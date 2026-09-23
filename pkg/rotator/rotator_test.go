// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rotator

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/gke-workload-identity-ca-rotation/pkg/config"
)

func setIntegrationEnv() {
	if os.Getenv("PROJECT_ID") == "" {
		os.Setenv("PROJECT_ID", "braided-tracker-464114-g8")
	}
	if os.Getenv("LOCATION") == "" {
		os.Setenv("LOCATION", "us-central1")
	}
	if os.Getenv("POOL_NAME") == "" {
		os.Setenv("POOL_NAME", "subordinate-ca-pool-us-central1")
	}
	if os.Getenv("ROOT_CA_NAME") == "" {
		os.Setenv("ROOT_CA_NAME", "root-ca-manual-staged")
	}
	if os.Getenv("ROOT_CA_POOL") == "" {
		os.Setenv("ROOT_CA_POOL", "root_ca_us")
	}
	if os.Getenv("ROOT_CA_LOCATION") == "" {
		os.Setenv("ROOT_CA_LOCATION", "us-central1")
	}
	if os.Getenv("QUEUE_ID") == "" {
		os.Setenv("QUEUE_ID", "ca-cleanup-queue-v2")
	}
	if os.Getenv("ROOT_QUEUE_ID") == "" {
		os.Setenv("ROOT_QUEUE_ID", "ca-root-cleanup-queue-v2")
	}
	if os.Getenv("CLEANUP_JOB_NAME") == "" {
		os.Setenv("CLEANUP_JOB_NAME", "ca-cleanup-job")
	}
}

func TestIntegration_RotateSubordinate(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()

	// Disable task scheduling to avoid side-effects during integration testing
	if os.Getenv("DISABLE_TASK_SCHEDULING") == "" {
		os.Setenv("DISABLE_TASK_SCHEDULING", "true")
	}

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	t.Logf("Running integration test against Project: %s, Pool: %s", cfg.ProjectID, cfg.CAPoolName)

	parentPool := fmt.Sprintf("projects/%s/locations/%s/caPools/%s", cfg.ProjectID, cfg.Location, cfg.CAPoolName)
	enabledCAs, err := r.listEnabledCAs(ctx, parentPool)
	if err != nil {
		t.Fatalf("Failed to list enabled CAs: %v", err)
	}
	// Align the expected count with the pool so this test exercises the rotation
	// logic rather than the CA count validation.
	r.cfg.CACount = len(enabledCAs)

	err = r.RotateSubordinate(ctx)
	if err != nil {
		t.Fatalf("RotateSubordinate failed: %v", err)
	}

	t.Log("Successfully completed RotateSubordinate integration test.")
}

func TestIntegration_ScheduleCleanup(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()
	if os.Getenv("CLEANUP_DELAY") == "" {
		os.Setenv("CLEANUP_DELAY", "1m")
	}

	// Make sure scheduling is enabled for this specific test
	os.Setenv("DISABLE_TASK_SCHEDULING", "false")

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	testCAName := "test-ca-for-scheduling-validation"
	t.Logf("Running integration test to schedule cleanup task for CA: %s", testCAName)

	err = r.ScheduleCleanup(ctx, testCAName)
	if err != nil {
		t.Fatalf("ScheduleCleanup failed: %v", err)
	}

	t.Log("Successfully scheduled the Cloud Task.")
}

func TestIntegration_RotateRootStage(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()

	// Disable task scheduling for the first test run
	if os.Getenv("DISABLE_TASK_SCHEDULING") == "" {
		os.Setenv("DISABLE_TASK_SCHEDULING", "false")
	}

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	t.Logf("Running Root Rotation Stage integration test against Project: %s", cfg.ProjectID)

	err = r.RotateRootStage(ctx)
	if err != nil {
		t.Fatalf("RotateRootStage failed: %v", err)
	}

	t.Log("Successfully completed RotateRootStage integration test.")
}

func TestIntegration_EnableNewRoot(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()

	if os.Getenv("NEW_ROOT") == "" {
		os.Setenv("NEW_ROOT", "projects/braided-tracker-464114-g8/locations/us-central1/caPools/root_ca_us/certificateAuthorities/root-ca-1781085152588")
	}

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	t.Logf("Running Enable New Root integration test")

	err = r.EnableNewRoot(ctx)
	if err != nil {
		t.Fatalf("EnableNewRoot failed: %v", err)
	}

	t.Log("Successfully completed EnableNewRoot integration test.")
}

func TestIntegration_FlipSubCA(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()

	if os.Getenv("ROTATION_PAIRS_JSON") == "" {
		os.Setenv("ROTATION_PAIRS_JSON", `[{"old": "projects/braided-tracker-464114-g8/locations/us-central1/caPools/subordinate-ca-pool-us-central1/certificateAuthorities/sub-ca-root-rot-1781080572477", "new": "projects/braided-tracker-464114-g8/locations/us-central1/caPools/subordinate-ca-pool-us-central1/certificateAuthorities/sub-ca-1781085167634"}]`)
	}
	if os.Getenv("ROTATION_REGION") == "" {
		os.Setenv("ROTATION_REGION", "us-central1")
	}

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	t.Logf("Running Sub CA Flip integration test")

	err = r.FlipSubCA(ctx)
	if err != nil {
		t.Fatalf("FlipSubCA failed: %v", err)
	}

	t.Log("Successfully completed FlipSubCA integration test.")
}

func TestIntegration_DisableOldRoot(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()

	if os.Getenv("OLD_ROOT") == "" {
		os.Setenv("OLD_ROOT", "projects/braided-tracker-464114-g8/locations/us-central1/caPools/root_ca_us/certificateAuthorities/root-ca-1781080553961")
	}

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	t.Logf("Running Disable Old Root integration test")

	err = r.DisableOldRoot(ctx)
	if err != nil {
		t.Fatalf("DisableOldRoot failed: %v", err)
	}

	t.Log("Successfully completed DisableOldRoot integration test.")
}

func TestIntegration_ScheduleRootCleanupTask(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()
	if os.Getenv("CLEANUP_DELAY") == "" {
		os.Setenv("CLEANUP_DELAY", "1m")
	}

	// Make sure scheduling is enabled for this specific test
	os.Setenv("DISABLE_TASK_SCHEDULING", "false")

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	oldRoot := "projects/braided-tracker-464114-g8/locations/us-central1/caPools/root_ca_us/certificateAuthorities/root-ca-1780994235016"
	pairsJSON := `[{"old": "projects/braided-tracker-464114-g8/locations/us-central1/caPools/subordinate-ca-pool-us-central1/certificateAuthorities/root-ca-manual-staged", "new": "projects/braided-tracker-464114-g8/locations/us-central1/caPools/subordinate-ca-pool-us-central1/certificateAuthorities/root-ca-1780994235016"}]`

	t.Logf("Running integration test to schedule Root CA cleanup task")

	testTaskID := fmt.Sprintf("test-root-cleanup-%d", time.Now().Unix())
	err = r.ScheduleRootCleanupTask(ctx, oldRoot, pairsJSON, testTaskID)
	if err != nil {
		t.Fatalf("ScheduleRootCleanupTask failed: %v", err)
	}

	t.Log("Successfully scheduled the Root CA Cleanup Cloud Task.")
}

func TestIntegration_BootstrapPool(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()
	// Set a pool name that we expect to be empty for this test
	if os.Getenv("POOL_NAME") == "" {
		os.Setenv("POOL_NAME", "subordinate-ca-pool-empty-test")
	}

	// Disable task scheduling
	os.Setenv("DISABLE_TASK_SCHEDULING", "true")

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	parentPool := fmt.Sprintf("projects/%s/locations/%s/caPools/%s", cfg.ProjectID, cfg.Location, cfg.CAPoolName)

	t.Logf("Running integration test for bootstrapping empty pool %s with CA_COUNT=%d", cfg.CAPoolName, cfg.CACount)

	err = r.RotateSubordinate(ctx)
	if err != nil {
		t.Fatalf("BootstrapPool failed: %v", err)
	}

	enabledCAs, err := r.listEnabledCAs(ctx, parentPool)
	if err != nil {
		t.Fatalf("Failed to list enabled CAs: %v", err)
	}
	if len(enabledCAs) != cfg.CACount {
		t.Errorf("Expected pool to be bootstrapped with %d active CA(s), got %d", cfg.CACount, len(enabledCAs))
	}

	t.Log("Successfully completed BootstrapPool integration test.")
}

func TestIntegration_CACountMismatch(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()

	// Disable task scheduling to avoid side-effects during integration testing
	if os.Getenv("DISABLE_TASK_SCHEDULING") == "" {
		os.Setenv("DISABLE_TASK_SCHEDULING", "true")
	}

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	parentPool := fmt.Sprintf("projects/%s/locations/%s/caPools/%s", cfg.ProjectID, cfg.Location, cfg.CAPoolName)

	enabledCAs, err := r.listEnabledCAs(ctx, parentPool)
	if err != nil {
		t.Fatalf("Failed to list enabled CAs: %v", err)
	}
	// This test exercises the alert-only mismatch path, which only applies to a
	// pool that already has at least one CA; a completely empty pool is bootstrapped
	// instead (see TestIntegration_BootstrapPool).
	if len(enabledCAs) == 0 {
		t.Fatalf("Pool %s has no active CAs; run this test against a pool that already has at least one active CA", cfg.CAPoolName)
	}

	// Deliberately configure an expected count that does NOT match the pool.
	r.cfg.CACount = len(enabledCAs) + 1

	t.Logf("Running integration test with expected CA_COUNT=%d against a pool with %d active CA(s)",
		r.cfg.CACount, len(enabledCAs))

	// A mismatch must not fail the run: the job still succeeds and rotates the
	// CAs actually present in the pool, while logging a stable alert line for
	// the log-match alert policy in terraform/monitoring.tf.
	err = r.RotateSubordinate(ctx)
	if err != nil {
		t.Fatalf("RotateSubordinate failed despite CA count mismatch (expected success): %v", err)
	}

	t.Log("RotateSubordinate succeeded on CA count mismatch, as expected.")
}

func TestIntegration_BootstrapRootPool(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	setIntegrationEnv()

	// Override specific root CA settings for bootstrapping
	os.Setenv("ROOT_CA_POOL", "root-bootstrap-test-pool")
	os.Setenv("ROOT_CA_SUBJECT_CN", "Test Automation Root")
	os.Setenv("ROOT_CA_SUBJECT_ORG", "Integration Tests")
	os.Setenv("DISABLE_TASK_SCHEDULING", "true")

	ctx := context.Background()
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to load configuration: %v", err)
	}

	r, err := NewRotator(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create Rotator: %v", err)
	}
	defer r.Close()

	t.Logf("Running integration test for bootstrapping empty ROOT pool: %s", cfg.RootCAPool)

	err = r.RotateRootStage(ctx)
	if err != nil {
		t.Fatalf("BootstrapRootPool failed: %v", err)
	}

	t.Log("Successfully completed BootstrapRootPool integration test.")
}
