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

# ------------------------------------------------------------------------------
# Monitoring & Alerting
# ------------------------------------------------------------------------------

resource "google_monitoring_notification_channel" "email_alert" {
  display_name = "CA Automation Alerts"
  type         = "email"
  labels = {
    email_address = var.alert_email
  }
}

resource "google_monitoring_alert_policy" "job_failure_alert" {
  display_name = "CA Automation Job Failure"
  combiner     = "OR"
  notification_channels = [google_monitoring_notification_channel.email_alert.name]
  
  conditions {
    display_name = "Cloud Run Job Execution Failed"
    condition_threshold {
      filter          = "resource.type = \"cloud_run_job\" AND resource.labels.job_name = starts_with(\"ca-\") AND metric.type = \"run.googleapis.com/job/completed_execution_count\" AND metric.labels.result = \"failed\""
      duration        = "60s"
      comparison      = "COMPARISON_GT"
      threshold_value = 0
      
      aggregations {
        alignment_period     = "300s" # 5 minutes
        cross_series_reducer = "REDUCE_SUM"
        per_series_aligner   = "ALIGN_SUM"
      }
    }
  }

  documentation {
    content   = "The CA Automation Cloud Run Job execution FAILED. Check Cloud Logging for the specific error."
    mime_type = "text/markdown"
  }
}

# The rotator logs a stable "ALERT: CA count mismatch" line whenever a Subordinate
# CA pool's number of active CAs doesn't match the configured ca_count (CA_COUNT env
# var on the ca-rotator-job). The mismatch never blocks the rotation, which operates
# on the CAs actually present in the pool; this policy turns that log line into an
# incident notification so the drift gets reconciled.
resource "google_monitoring_alert_policy" "ca_count_mismatch_alert" {
  display_name = "CA Count Mismatch"
  combiner     = "OR"
  notification_channels = [google_monitoring_notification_channel.email_alert.name]

  conditions {
    display_name = "Subordinate pool active CA count differs from configured CA_COUNT"
    condition_matched_log {
      filter = "resource.type=\"cloud_run_job\" AND resource.labels.job_name=\"ca-rotator-job\" AND textPayload=~\"^ALERT: CA count mismatch\""
    }
  }

  documentation {
    content   = "A Subordinate CA pool's active CA count doesn't match the configured ca_count. The rotation still ran over the CAs actually present. Reconcile by creating or removing CAs, or by updating ca_count in terraform.tfvars. Check Cloud Logging for the 'ALERT: CA count mismatch' line to see the pool, actual, and expected counts."
    mime_type = "text/markdown"
  }
}


