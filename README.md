# GKE MWLID Automated CA Lifecycle Management

An automated "Robotic SRE" system built in Go to manage the lifecycle of Google Cloud Private Certificate Authorities. It is specifically designed to support GKE Managed Workload Identity (MWLID) with zero-downtime rolling rotations.

## Architecture

The system uses a **Chained Job Design** to handle the required grace periods:
`Cloud Scheduler (Cron) -> Rotator (Cloud Run Job) -> Cloud Tasks (Delayed) -> Cleanup (Cloud Run Job)`

- **Rotator:** Discovers active CAs, creates/activates new ones via Root CA signing, performs a zero-downtime flip (Enable New, Disable Old), and schedules a cleanup task.
- **Cleanup (Sweeper):** Triggered by Cloud Tasks after a 48-hour grace period to permanently delete disabled CAs.
- **Security:** Uses a single dedicated Service Account (`ca-automation-bot`) with minimum viable permissions.

## Project Structure

- `cmd/main.go`: Application entry point with `ROTATE` and `CLEANUP` modes.
- `pkg/config/`: Configuration management using environment variables.
- `pkg/rotator/`: Core logic for rolling CA rotation and Cloud Tasks orchestration.
- `pkg/sweeper/`: Logic for safe CA deletion.
- `terraform/`: Infrastructure as Code to deploy the entire stack.

## Getting Started

### 1. Build and Push Image
You can build the image manually or use the provided Cloud Build configuration for a fully automated CI/CD pipeline.

**Manual Build:**
```bash
gcloud builds submit --tag us-central1-docker.pkg.dev/[PROJECT_ID]/cloud-run-source-deploy/ca-automation:latest .
```

**Automated CI/CD (Cloud Build):**
The `cloudbuild.yaml` file provides a fully automated pipeline. It builds the image, tags it with the commit SHA, and updates all four Cloud Run jobs.

*   **Multi-Region Support:** The Subordinate jobs are updated across all regions specified in the `_SUB_LOCATIONS` variable.
*   **Mandatory IAM Setup:** To allow Cloud Build to update the jobs, you must grant it permission to "use" the Cloud Run job's service account:
    ```bash
    gcloud iam service-accounts add-iam-policy-binding [JOB_SERVIECE_ACCOUNT] \
      --member="serviceAccount:[CLOUDBUILD_SA_EMAIL]" \
      --role="roles/iam.serviceAccountUser"
    ```

**Run Build:**
```bash
# Default build (us-central1 only)
gcloud builds submit --config cloudbuild.yaml --substitutions=COMMIT_SHA=$(git rev-parse --short HEAD) .

# Multi-region build
gcloud builds submit --config cloudbuild.yaml \
  --substitutions=COMMIT_SHA=$(git rev-parse --short HEAD),_SUB_LOCATIONS="us-central1 europe-north1 asia-southeast1" .
```



### 2. Deploy Infrastructure

Before applying the Terraform configuration, you must define your environment-specific values. 

1. Create a `terraform.tfvars` file in the `terraform/` directory.
2. Populate it with your project details, Docker image URL (from Step 1), and your regional CA Pool map.

**Example `terraform.tfvars`:**
```hcl
project_id         = "your-gcp-project-id"
region             = "us-central1"
docker_image       = "us-central1-docker.pkg.dev/.../ca-automation:latest"
root_ca_pool       = "your-root-pool-name"
root_ca_location   = "us-central1"
alert_email        = "sre-team@yourcompany.com"

# Map your regional Subordinate pools
# ca_count (optional, default 1) = the expected number of active intermediate
# CAs in the pool; the rotator alerts when the pool doesn't match.
subordinate_pools  = {
  "us-central1"     = { pool_name = "subordinate-ca-pool-us-central1" }
  "europe-north1"   = { pool_name = "subordinate-ca-pool-europe-north1", ca_count = 2 }
  "asia-southeast1" = { pool_name = "subordinate-ca-pool-asia-southeast1" }
}
```
*Note: The specific name of the active Root CA is auto-discovered by the Go agent at runtime. You do not need to provide it as a parameter.*

Once your `.tfvars` file is configured, initialize and deploy:
```bash
cd terraform
terraform init
terraform apply
```

### 3. Update Jobs (Manual Code Push)
If you update the Go code and want to redeploy to the existing jobs without running a full Terraform apply:
```bash
# Update Subordinate jobs
gcloud run jobs update ca-rotator-job --image=[IMAGE_URL]:latest --region=us-central1
gcloud run jobs update ca-cleanup-job --image=[IMAGE_URL]:latest --region=us-central1

# Update Root transition jobs
gcloud run jobs update ca-root-rotator-job --image=[IMAGE_URL]:latest --region=us-central1
gcloud run jobs update ca-root-cleanup-job --image=[IMAGE_URL]:latest --region=us-central1
```

## Root CA Rotation Orchestration

**Note: The Root CA rotation scheduler (`root-ca-rotation-trigger`) is PAUSED by default for safety. You must either trigger it manually via the console/gcloud or set it to RESUME in Terraform to start the automated multi-year cycle.**

Once the `ca-root-rotator-job` has been manually triggered to initiate Phase 1 (Global Staging), the system automatically schedules the remaining four phases in the `ca-root-flip-cleanup-queue`. 

While these phases are scheduled with safe delays (2h to 72h), an SRE can manually trigger them in sequence if they wish to accelerate the transition after verifying trust propagation.

### Manual Orchestration Steps:

**1. Enable New Root CA (T+2h):**
Activates the new Root CA so it can be used for signing. 
```bash
gcloud tasks tasks run [TASK_ID] --queue=ca-root-flip-cleanup-queue-[SUFFIX] --location=us-central1
```

**2. Flip Regional Subordinate CAs (T+3h, T+4h...):**
Performs the zero-downtime flip for each region, enabling the new regional Sub CAs (signed by the new Root) and disabling the old ones. Execute these in regional order.
```bash
gcloud tasks tasks run [TASK_ID] --queue=ca-root-flip-cleanup-queue-[SUFFIX] --location=us-central1
```

**3. Disable Old Root CA (T+Last Region + 1h):**
Disables the old Root CA, preventing it from signing any further certificates while keeping it in the trust bundle for validation.
```bash
gcloud tasks tasks run [TASK_ID] --queue=ca-root-flip-cleanup-queue-[SUFFIX] --location=us-central1
```

**4. Final Global Cleanup (T+72h):**
The nuclear step. Permanently deletes the old regional Sub CAs and finally deletes the old Root CA.
```bash
gcloud tasks tasks run [TASK_ID] --queue=ca-root-flip-cleanup-queue-[SUFFIX] --location=us-central1
```

## Operations & "Break Glass"

### Bootstrapping Initial Infrastructure (Initial Seeding)
The system is designed to handle entirely **empty CA pools** automatically. You do not need to manually create the first CA in your project.

#### 1. Bootstrap the Root CA
If the `ca-root-rotator-job` triggers and finds a completely empty Root CA pool:
1.  It automatically creates a foundational `SELF_SIGNED` Root CA.
2.  It uses the `root_ca_subject_cn` and `root_ca_subject_org` parameters from your configuration.
3.  It defaults to `RSA_PKCS1_4096_SHA256` unless `root_ca_algorithm` is specified.
4.  It immediately moves the Root CA to `ENABLED`.

To bootstrap the global Root:
```bash
gcloud run jobs execute ca-root-rotator-job --region=us-central1
```

#### 2. Bootstrap Regional Subordinates
If the `ca-rotator-job` triggers and finds a pool with **zero CAs of any kind** (active, staged, or awaiting activation):
1.  It automatically fetches the baseline configuration (`Config`, `Lifetime`, `KeySpec`) from your central Root CA (via auto-discovery).
2.  It creates, signs, and enables `ca_count` initial Subordinate CA(s) in that pool (default 1).
3.  No cleanup tasks are scheduled since there is no old CA to replace.

To bootstrap a new region manually after adding it to Terraform (with the desired `ca_count`):
```bash
gcloud run jobs execute ca-rotator-job --region=[NEW_REGION]
```

#### 3. Expected CA Count (Alerting on Drift)
`ca_count` is the **expected number of active intermediate CAs** for a pool, used both to bootstrap an empty pool (above) and, on every subsequent run, to detect drift on a pool that already has at least one CA:

- On a mismatch, the job logs an `ALERT: CA count mismatch` line, which the "CA Count Mismatch" Cloud Monitoring policy (a log-match alert in `terraform/monitoring.tf`) turns into an email to `alert_email`. The alert fires again on every run until the drift is fixed.
- The mismatch never blocks anything: the run still succeeds and rotates the CAs actually present in the pool, whatever their number. Once a pool has been bootstrapped, the rotator never creates or deletes CAs to fix drift — pool resizing from then on is an operator decision (create or remove CAs manually); keep `ca_count` in sync in your `terraform.tfvars` whenever you do.

The rolling rotation replaces each active CA one by one, so a pool with N active CAs keeps N active CAs going forward.

To bootstrap a new region manually after adding it to Terraform:
```bash
gcloud run jobs execute ca-rotator-job --region=[NEW_REGION]
```

### Manual Rotation
To force a rotation immediately outside of the schedule:

**Subordinate Rotation:**
```bash
gcloud run jobs execute ca-rotator-job --region=us-central1
```

**Root CA Rotation (Phase 1):**
```bash
gcloud run jobs execute ca-root-rotator-job --region=us-central1
```

### Emergency Rotation (Disable Only)
To rotate CAs but **skip** scheduling the automatic deletion (Audit Mode):
```bash
gcloud run jobs execute ca-rotator-job \
  --region=us-central1 \
  --update-env-vars DISABLE_TASK_SCHEDULING=true
```

## Task Management & Orchestration

The automation utilizes **Google Cloud Tasks** to handle long-running grace periods (like the 2-hour propagation buffer and the 48-hour deletion delay). 

Every scheduled task generated by the system is assigned a deterministic, human-readable ID (e.g., `enable-new-root-168641...`, `flip-sub-ca-us-central1-...`). This ensures strict idempotency and gives operators clear visibility into exactly what actions are pending in the queue.

### Centralized Orchestration Architecture
Because Google Cloud Tasks and Cloud Scheduler are not available in every GCP region (e.g., `europe-north1`), this system uses a **Centralized Orchestration Architecture**. While the CA Pools and Cloud Run jobs remain regional for performance and data residency, all orchestration triggers (Scheduler) and queues (Tasks) are hosted in your primary region (typically `us-central1`). 

*   **Cloud Scheduler:** Lives in the central region and fires cross-region authenticated HTTP POST requests to trigger the regional Cloud Run jobs.
*   **Cloud Tasks:** The Go application uses the `QUEUE_LOCATION` environment variable to bridge the gap, ensuring that regional jobs can always communicate with their central "waiting room" queues without throwing 404 errors.

### Resiliency and The Two-Layer Retry System
The system is built to survive transient errors, crashes, and regional outages using a two-layer retry architecture:

1. **The Inner Loop (Cloud Run):** If a job crashes or returns an error during execution, Cloud Run will immediately retry the exact container execution up to **3 times** (`max_retries = 3` in Terraform). 
2. **The Outer Loop (Cloud Tasks):** If a delayed task is triggered by the queue and fails entirely (e.g., Cloud Run exhausts its 3 inner retries, or the region is down), Cloud Tasks acts as the ultimate safety net. It will retry the webhook delivery up to **5 times** using exponential backoff (up to 1 hour between attempts). 

*Note: The outer loop only applies to automated executions triggered by Cloud Tasks or Cloud Scheduler. If you manually run `gcloud run jobs execute`, you will only see the 3 inner-loop retries before the execution permanently stops.*

### The Paused Queue Safety Net
By default, all Cloud Tasks queues (`ca-cleanup-queue` and `ca-root-flip-cleanup-queue`) are deployed with `desired_state = "PAUSED"` via Terraform.

This means that while the Go application will successfully **create** and **schedule** cleanup tasks in the queue, Cloud Tasks will **not automatically dispatch them** when their delay timer expires. The tasks will accumulate safely in the queue.

This gives operators the ability to manually verify that the new CAs are functioning correctly before permanent deletion occurs. You have two ways to execute tasks that are sitting in a paused queue:

1.  **Manual Override:** You can force a specific task to run immediately via the CLI (see below).
2.  **The Floodgate:** You can resume the entire queue. Any tasks whose timer has already expired will be instantly dispatched according to the configured rate limits:
    ```bash
    gcloud tasks queues resume [QUEUE_NAME] --location=us-central1
    ```

### Managing Tasks Manually

If you need to intervene, you can safely interact with the queue via the `gcloud` CLI:

**1. Force a task to run immediately (Skip the delay):**
If you don't want to wait 48 hours for a cleanup, you can force the task to execute right now:
```bash
gcloud tasks tasks run [TASK_ID] \
  --queue=ca-root-flip-cleanup-queue-v2 \
  --location=us-central1
```

**2. Abort/Delete a specific task:**
If you want to cancel a specific regional flip or cleanup:
```bash
gcloud tasks tasks delete [TASK_ID] \
  --queue=ca-root-flip-cleanup-queue-v2 \
  --location=us-central1
```

**3. Purge the entire queue (Nuclear Option):**
If you want to completely abort an ongoing Root CA rotation and clear all pending orchestration tasks instantly:
```bash
gcloud tasks queues purge ca-root-flip-cleanup-queue-v2 \
  --location=us-central1
```

## Configuration (terraform.tfvars)

The system is configured centrally via Terraform. Update your `terraform.tfvars` file to manage these settings:

| Variable | Description | Default / Example |
| :--- | :--- | :--- |
| `project_id` | Your GCP Project ID | Required |
| `region` | GCP Region for infrastructure and primary Subordinate Pool | `us-central1` |
| `docker_image` | The Artifact Registry URL for the compiled Go agent | Required |
| `root_ca_pool` | The global Root CA Pool ID | Required |
| `root_ca_location` | The GCP Region where the Root Pool resides | `us-central1` |
| `root_ca_subject_cn` | The Common Name (CN) for bootstrapping the Root CA | Required |
| `root_ca_subject_org` | The Organization (O) for bootstrapping the Root CA | Required |
| `root_ca_algorithm` | The Key Algorithm for the Root CA | `RSA_PKCS1_4096_SHA256` |
| `subordinate_pools` | Map of regions to their corresponding Subordinate CA pool names. Each pool may optionally set `ca_count` (whole number >= 1, default 1) as the expected number of active intermediate CAs; used to bootstrap an empty pool and to alert on drift afterward | Required |
| `rotation_schedule` | Cron schedule for the primary automated Subordinate rotation | `0 0 1 */2 *` |
| `service_account_id` | Name of the IAM Service Account created for the agent | `ca-automation-bot` |
| `cleanup_delay` | Grace period before deleting old Subordinate CAs | `48h` |
| `root_cleanup_delay` | Grace period before permanently deleting old Root CAs | `72h` |
| `staging_buffer` | Time to wait for GKE nodes to fetch STAGED CAs (Not currently used as a delay) | `2h` |
| `alert_email` | Destination email for Cloud Monitoring alerts | Required |

## Testing

Integration tests interact with real Google Cloud APIs. You must have valid Application Default Credentials and the required IAM permissions.

### 1. Sequential Rolling Rotation
Test the core logic that discovers the oldest active CA, performs the flip, and clones the configuration. 
Note: By default, this test disables task scheduling to avoid side-effects.
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_RotateSubordinate -v -count=1
```

### 2. End-to-End Task Scheduling
Test the integration with Cloud Tasks to ensure the cleanup trigger is correctly scheduled with an OAuth token and a JSON payload.
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_ScheduleCleanup -v -count=1
```

### 3. Manual Cleanup (Sweeper)
Directly test the logic that deletes a specific `DISABLED` or `STAGED` CA from your pool.
```bash
RUN_INTEGRATION_TESTS=1 TEST_CA_TO_DELETE="your-ca-id" go test ./pkg/sweeper/... -v -count=1
```

### 4. End-to-End Dry Run (No Side Effects)
You can run the rotation test but explicitly force it to skip both the CA flip and the task scheduling by using the built-in config flags:
```bash
RUN_INTEGRATION_TESTS=1 DISABLE_TASK_SCHEDULING=true go test ./pkg/rotator/... -v -count=1
```

### 5. Root Phase 1 (Stage) Test
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_RotateRootStage -v -count=1
```

### 6. Enable New Root CA Test
*Note: Requires valid NEW_ROOT environment variable.*
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_EnableNewRoot -v -count=1
```

### 7. Regional Sub CA Flip Test
*Note: Requires valid ROTATION_PAIRS_JSON environment variable.*
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_FlipSubCA -v -count=1
```

### 8. Disable Old Root CA Test
*Note: Requires valid OLD_ROOT environment variable.*
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_DisableOldRoot -v -count=1
```

### 9. Root CA Cleanup Task Scheduling
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_ScheduleRootCleanupTask -v -count=1
```

### 10. Global Root Chain Cleanup (Sweeper)
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/sweeper/... -run TestIntegration_CleanupRoot -v -count=1
```

### 11. Pool Bootstrap (Empty Pool)
Test that an entirely empty pool is bootstrapped with `ca_count` initial Subordinate CA(s) in a single run.
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_BootstrapPool -v -count=1
```

### 12. CA Count Mismatch (Alert, Still Rotates)
Test that a mismatch between an already-seeded pool's active CA count and `CA_COUNT` does not fail the run — the rotation still succeeds, and the mismatch is only logged as an alert line. Requires the pool to already have at least one active CA.
```bash
RUN_INTEGRATION_TESTS=1 go test ./pkg/rotator/... -run TestIntegration_CACountMismatch -v -count=1
```

## Verifying mTLS in GKE

To verify that GKE workloads correctly receive certificates and trust the new CA chain after a rotation:

1.  **Deploy Test Pods:**
    ```bash
    kubectl apply -f test-pods.yaml
    ```

2.  **Get Server IP:**
    ```bash
    SERVER_IP=$(kubectl get pod mtls-server -n ca-test -o jsonpath='{.status.podIP}')
    ```

3.  **Execute mTLS Handshake from Client:**
    ```bash
    kubectl exec -it mtls-client -n ca-test -- openssl s_client \
      -connect ${SERVER_IP}:8443 \
      -cert /var/run/secrets/workload-spiffe-credentials/certificates.pem \
      -key /var/run/secrets/workload-spiffe-credentials/private_key.pem \
      -CAfile /var/run/secrets/workload-spiffe-credentials/ca_certificates.pem
    ```
    If the connection is successful, it confirms the pod certificates are valid and chain correctly to the root of trust managed by the automation.

## System Components (Cloud Run Jobs)

The automation is split across four distinct Cloud Run jobs. This separation of concerns ensures that routine Subordinate CA rotations are completely isolated from high-stakes Root CA transitions.

### 1. `ca-rotator-job` (Routine Subordinate Rotation)
*   **Purpose:** Handles the frequent, routine rotation of regional Subordinate CAs (e.g., every 2 months).
*   **Trigger:** Automatically triggered by Cloud Scheduler.
*   **Action:** Bootstraps `ca_count` initial CAs in an entirely empty pool; otherwise discovers the oldest active Subordinate CA, creates a new one, signs it with the Root CA, performs a zero-downtime flip (Enable New, Disable Old). The pool's active CA count is compared against the configured `ca_count`; a mismatch logs an alert line (picked up by the "CA Count Mismatch" monitoring policy) but the rotation proceeds over the CAs actually present.
*   **Tasks Created:** Schedules a single task in `ca-cleanup-queue` with `OPERATION=CLEANUP` for T+48 hours to permanently delete the old Subordinate CA.

### 2. `ca-cleanup-job` (Routine Subordinate Cleanup)
*   **Purpose:** The "Sweeper" for routine rotations.
*   **Trigger:** Triggered by Cloud Tasks via the `ca-cleanup-queue`.
*   **Action:** Receives the `CA_TO_DELETE` environment variable override and permanently deletes the specified `DISABLED` Subordinate CA, removing it from the pool.

### 3. `ca-root-rotator-job` (Global Root Orchestrator)
*   **Purpose:** Handles the complex, multi-region state machine required to rotate the foundational Root CA (e.g., every 5 years).
*   **Trigger:** **Manual execution by an SRE.** (The Cloud Scheduler trigger `root-ca-rotation-trigger` is provided in the infrastructure but is **paused by default** for safety).
*   **Action:** Creates a new global Root CA (`STAGED`) and new Subordinate CAs (`STAGED`) in every configured region simultaneously.
*   **Tasks Created:** Schedules a highly staggered orchestration sequence in `ca-root-flip-cleanup-queue`:
    1.  `ENABLE_NEW_ROOT` (T+2h)
    2.  `FLIP_SUB_CA_AGAINST_NEW_ROOT` (T+3h, T+4h... staggered per region)
    3.  `DISABLE_OLD_ROOT` (T+Last Region + 1h)
    4.  `CLEANUP_ROOT` (T+72h)

### 4. `ca-root-cleanup-job` (Global Root Cleanup)
*   **Purpose:** The final, permanent deletion step for a Root CA transition.
*   **Trigger:** Triggered by Cloud Tasks via the `ca-root-flip-cleanup-queue` after a 72-hour delay.
*   **Action:** Receives a JSON payload of all regional CA pairs and the Old Root CA. It iterates through all regions to permanently delete the old Subordinate CAs, and then permanently deletes the Old Root CA.
