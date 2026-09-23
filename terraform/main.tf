# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

terraform {
  required_version = ">= 1.3.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 7.17.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

data "google_project" "project" {}

# ------------------------------------------------------------------------------
# Service Account & IAM
# ------------------------------------------------------------------------------

resource "google_service_account" "ca_automation" {
  account_id   = var.service_account_id
  display_name = "CA Automation Agent"
  description  = "Service account used by Cloud Run and Cloud Tasks to manage Private CA rotation"
}

# The Service Account needs to act as itself to generate OIDC/OAuth tokens
resource "google_service_account_iam_member" "token_creator" {
  service_account_id = google_service_account.ca_automation.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.ca_automation.email}"
}

# The Service Account needs permission to pass its identity to Cloud Tasks
resource "google_service_account_iam_member" "service_account_user" {
  service_account_id = google_service_account.ca_automation.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.ca_automation.email}"
}

# Allow Cloud Build (using the Compute Engine default SA) to update Cloud Run jobs as the bot
resource "google_service_account_iam_member" "cloudbuild_sa_user" {
  service_account_id = google_service_account.ca_automation.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${data.google_project.project.number}-compute@developer.gserviceaccount.com"
}



# The agent needs permission to manage CAs
resource "google_project_iam_member" "privateca_admin" {
  project = var.project_id
  role    = "roles/privateca.admin"
  member  = "serviceAccount:${google_service_account.ca_automation.email}"
}

# The agent needs permission to enqueue tasks
resource "google_project_iam_member" "tasks_enqueuer" {
  project = var.project_id
  role    = "roles/cloudtasks.enqueuer"
  member  = "serviceAccount:${google_service_account.ca_automation.email}"
}

# The Cloud Task (and potentially humans) needs permission to invoke the Cloud Run Jobs via REST API
resource "google_project_iam_member" "run_developer" {
  project = var.project_id
  role    = "roles/run.developer"
  member  = "serviceAccount:${google_service_account.ca_automation.email}"
}

# ------------------------------------------------------------------------------
# Cloud Tasks Queues
# ------------------------------------------------------------------------------

# Use a random suffix to avoid the 7-day "tombstone" limitation on queue name reuse.
resource "random_id" "queue_suffix" {
  byte_length = 4
}

# Regional queues for Subordinate CA cleanups
# NOTE: These are all centralized in var.region because Cloud Tasks is not available in every region (like europe-north1).
resource "google_cloud_tasks_queue" "ca_cleanup_queue" {
  for_each = var.subordinate_pools
  
  name     = "ca-cleanup-queue-${each.key}-${random_id.queue_suffix.hex}"
  location = var.region
  desired_state = "PAUSED"

  rate_limits {
    max_concurrent_dispatches = 10
    max_dispatches_per_second = 10
  }

  retry_config {
    max_attempts       = 5
    max_backoff        = "3600s" 
    min_backoff        = "5s"
    max_doublings      = 16
  }
}

# Centralized queue for Root CA orchestration
resource "google_cloud_tasks_queue" "ca_root_cleanup_queue" {
  name     = "ca-root-flip-cleanup-queue-${random_id.queue_suffix.hex}"
  location = var.region
  desired_state = "PAUSED"

  rate_limits {
    max_concurrent_dispatches = 5
    max_dispatches_per_second = 5
  }

  retry_config {
    max_attempts       = 5
    max_backoff        = "3600s"
    min_backoff        = "5s"
    max_doublings      = 16
  }
}

# ------------------------------------------------------------------------------
# Cloud Run Jobs (Subordinate CA Automation - Regional)
# ------------------------------------------------------------------------------

resource "google_cloud_run_v2_job" "ca_cleanup_job" {
  for_each = var.subordinate_pools
  
  name     = "ca-cleanup-job"
  location = each.key
  deletion_protection = false

  template {
    template {
      max_retries = 3
      service_account = google_service_account.ca_automation.email
      containers {
        image = var.docker_image
        env {
          name  = "PROJECT_ID"
          value = var.project_id
        }
        env {
          name  = "LOCATION"
          value = each.key
        }
        env {
          name  = "POOL_NAME"
          value = each.value.pool_name
        }

        env {
          name  = "ROOT_CA_POOL"
          value = var.root_ca_pool
        }
        env {
          name  = "ROOT_CA_LOCATION"
          value = var.root_ca_location
        }
        env {
          name  = "QUEUE_ID"
          value = google_cloud_tasks_queue.ca_cleanup_queue[each.key].name
        }
        env {
          name  = "ROOT_QUEUE_ID"
          value = google_cloud_tasks_queue.ca_root_cleanup_queue.name
        }
        env {
          name  = "QUEUE_LOCATION" # The physical location where the Cloud Tasks queue resides
          value = var.region
        }
        env {
          name  = "SERVICE_ACCOUNT_EMAIL"
          value = google_service_account.ca_automation.email
        }
      }
    }
  }
}

resource "google_cloud_run_v2_job" "ca_rotator_job" {
  for_each = var.subordinate_pools
  
  name     = "ca-rotator-job"
  location = each.key
  deletion_protection = false

  template {
    template {
      max_retries = 3
      service_account = google_service_account.ca_automation.email
      containers {
        image = var.docker_image
        env {
          name  = "OPERATION"
          value = "ROTATE"
        }
        env {
          name  = "PROJECT_ID"
          value = var.project_id
        }
        env {
          name  = "LOCATION"
          value = each.key
        }
        env {
          name  = "POOL_NAME"
          value = each.value.pool_name
        }
        env {
          name  = "CA_COUNT"
          value = tostring(each.value.ca_count)
        }

        env {
          name  = "ROOT_CA_POOL"
          value = var.root_ca_pool
        }
        env {
          name  = "ROOT_CA_LOCATION"
          value = var.root_ca_location
        }
        env {
          name  = "QUEUE_ID"
          value = google_cloud_tasks_queue.ca_cleanup_queue[each.key].name
        }
        env {
          name  = "ROOT_QUEUE_ID"
          value = google_cloud_tasks_queue.ca_root_cleanup_queue.name
        }
        env {
          name  = "QUEUE_LOCATION" # The physical location where the Cloud Tasks queue resides
          value = var.region
        }
        env {
          name  = "CLEANUP_DELAY"
          value = var.cleanup_delay
        }
        env {
          name  = "STAGING_BUFFER"
          value = var.staging_buffer
        }
        env {
          name  = "SERVICE_ACCOUNT_EMAIL"
          value = google_service_account.ca_automation.email
        }
      }
    }
  }
}

# ------------------------------------------------------------------------------
# Cloud Run Jobs (Root CA Automation - Centralized)
# ------------------------------------------------------------------------------

resource "google_cloud_run_v2_job" "ca_root_cleanup_job" {
  name     = "ca-root-cleanup-job"
  location = var.region
  deletion_protection = false

  template {
    template {
      max_retries = 3
      service_account = google_service_account.ca_automation.email
      containers {
        image = var.docker_image
        env {
          name  = "PROJECT_ID"
          value = var.project_id
        }
        env {
          name  = "LOCATION"
          value = var.region
        }
        env {
          name  = "POOL_NAME"
          value = "global-root-pool" # Not actively used by Root orchestrator, required by config
        }

        env {
          name  = "ROOT_CA_POOL"
          value = var.root_ca_pool
        }
        env {
          name  = "ROOT_CA_LOCATION"
          value = var.root_ca_location
        }
        env {
          name  = "QUEUE_ID"
          value = "global-root-queue" # Not actively used by Root orchestrator, required by config
        }
        env {
          name  = "ROOT_QUEUE_ID"
          value = google_cloud_tasks_queue.ca_root_cleanup_queue.name
        }
        env {
          name  = "QUEUE_LOCATION" # The physical location where the Cloud Tasks queue resides
          value = var.region
        }
        env {
          name  = "ROOT_CLEANUP_JOB_NAME"
          value = "ca-root-cleanup-job"
        }
        env {
          name  = "SERVICE_ACCOUNT_EMAIL"
          value = google_service_account.ca_automation.email
        }
      }
    }
  }
}

resource "google_cloud_run_v2_job" "ca_root_rotator_job" {
  name     = "ca-root-rotator-job"
  location = var.region
  deletion_protection = false

  template {
    template {
      max_retries = 3
      service_account = google_service_account.ca_automation.email
      containers {
        image = var.docker_image
        env {
          name  = "OPERATION"
          value = "ROTATE_ROOT_STAGE"
        }
        env {
          name  = "PROJECT_ID"
          value = var.project_id
        }
        env {
          name  = "LOCATION"
          value = var.region
        }
        env {
          name  = "POOL_NAME"
          value = "global-root-pool" # Not actively used by Root orchestrator, required by config
        }

        env {
          name  = "ROOT_CA_POOL"
          value = var.root_ca_pool
        }
        env {
          name  = "ROOT_CA_LOCATION"
          value = var.root_ca_location
        }
        env {
          name  = "QUEUE_ID"
          value = "global-root-queue" # Not actively used by Root orchestrator, required by config
        }
        env {
          name  = "ROOT_QUEUE_ID"
          value = google_cloud_tasks_queue.ca_root_cleanup_queue.name
        }
        env {
          name  = "QUEUE_LOCATION" # The physical location where the Cloud Tasks queue resides
          value = var.region
        }
        env {
          name  = "ROOT_CA_SUBJECT_CN"
          value = var.root_ca_subject_cn
        }
        env {
          name  = "ROOT_CA_SUBJECT_ORG"
          value = var.root_ca_subject_org
        }
        env {
          name  = "ROOT_CA_ALGORITHM"
          value = var.root_ca_algorithm
        }
        env {
          name  = "ROTATOR_JOB_NAME"
          value = "ca-root-rotator-job"
        }
        env {
          name  = "ROOT_CLEANUP_JOB_NAME"
          value = "ca-root-cleanup-job"
        }
        env {
          name  = "CLEANUP_DELAY"
          value = var.cleanup_delay
        }
        env {
          name  = "ROOT_CLEANUP_DELAY"
          value = var.root_cleanup_delay
        }
        env {
          name  = "SERVICE_ACCOUNT_EMAIL"
          value = google_service_account.ca_automation.email
        }
      }
    }
  }
}

# ------------------------------------------------------------------------------
# Cloud Scheduler Triggers
# ------------------------------------------------------------------------------

# Regional triggers for Subordinate CA rotation
# NOTE: These are centralized in var.region because Cloud Scheduler is not available in every region.
resource "google_cloud_scheduler_job" "ca_rotation_trigger" {
  for_each = var.subordinate_pools
  
  name        = "ca-rotation-trigger-${each.key}"
  description = "Triggers the routine Subordinate CA rotation for ${each.key}"
  schedule    = var.rotation_schedule
  time_zone   = "UTC"
  region      = var.region

  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/projects/${var.project_id}/locations/${each.key}/jobs/${google_cloud_run_v2_job.ca_rotator_job[each.key].name}:run"
    
    oauth_token {
      service_account_email = google_service_account.ca_automation.email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }
}

resource "google_cloud_scheduler_job" "root_ca_rotation_trigger" {
  name        = "root-ca-rotation-trigger"
  description = "Triggers the global Root CA rotation job (Phase 1)"
  paused      = true
  schedule    = "0 0 1 1 *"
  time_zone   = "UTC"
  region      = var.region

  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/projects/${var.project_id}/locations/${var.region}/jobs/${google_cloud_run_v2_job.ca_root_rotator_job.name}:run"
    
    oauth_token {
      service_account_email = google_service_account.ca_automation.email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }
}
