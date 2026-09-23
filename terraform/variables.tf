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

variable "project_id" {
  description = "The GCP Project ID"
  type        = string
}

variable "region" {
  description = "The central GCP region for root resources and orchestrator jobs"
  type        = string
  default     = "us-central1"
}

variable "subordinate_pools" {
  description = "Map of regions to their corresponding manually created Subordinate CA pools. Each pool may optionally set ca_count (default 1) as the expected number of active intermediate CAs; the rotator logs an alert when the pool doesn't match."
  type = map(object({
    pool_name = string
    ca_count  = optional(number, 1)
  }))
  default = {
    "us-central1" = {
      pool_name = "subordinate-ca-pool-us-central1"
    }
  }

  validation {
    condition = alltrue([
      for _, pool in var.subordinate_pools :
      pool.ca_count >= 1 && floor(pool.ca_count) == pool.ca_count
    ])
    error_message = "The ca_count of every subordinate pool must be a whole number >= 1."
  }
}

variable "docker_image" {
  description = "The container image for the automation agent"
  type        = string
}

variable "root_ca_pool" {
  description = "The name of the Root CA Pool"
  type        = string
}

variable "root_ca_location" {
  description = "The location of the Root CA Pool"
  type        = string
}

variable "root_ca_subject_cn" {
  description = "The Common Name (CN) for bootstrapping the Root CA"
  type        = string
}

variable "root_ca_subject_org" {
  description = "The Organization (O) for bootstrapping the Root CA"
  type        = string
}

variable "root_ca_algorithm" {
  description = "The Key Algorithm for the Root CA (e.g. RSA_PKCS1_4096_SHA256)"
  type        = string
  default     = "RSA_PKCS1_4096_SHA256"
}


variable "rotation_schedule" {
  description = "Cron schedule for the Cloud Scheduler to trigger the rotation"
  type        = string
  default     = "0 0 1 */2 *" # 1st of every 2nd month
}

variable "service_account_id" {
  description = "The ID of the service account to create for the automation"
  type        = string
  default     = "ca-automation-bot"
}

variable "cleanup_delay" {
  description = "The delay before deleting a disabled Subordinate CA (e.g., '48h', '1h', '1m')"
  type        = string
  default     = "48h"
}

variable "root_cleanup_delay" {
  description = "The delay before deleting a disabled Root CA (e.g., '48h')"
  type        = string
  default     = "72h"
}

variable "staging_buffer" {
  description = "The buffer period for staging a new CA (e.g., '2h')"
  type        = string
  default     = "2h"
}

variable "alert_email" {
  description = "Email address to receive CA Automation alerts"
  type        = string
}

