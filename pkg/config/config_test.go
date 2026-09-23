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

package config

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLoadConfig_Success(t *testing.T) {
	// Set required environment variables
	t.Setenv("PROJECT_ID", "test-project")
	t.Setenv("LOCATION", "us-central1")
	t.Setenv("POOL_NAME", "test-pool")
	t.Setenv("ROOT_CA_NAME", "test-root-ca")
	t.Setenv("ROOT_CA_POOL", "root-pool")
	t.Setenv("ROOT_CA_LOCATION", "us-central1")
	t.Setenv("CLEANUP_JOB_NAME", "ca-cleanup-job")
	t.Setenv("QUEUE_ID", "ca-rotation-queue")
	t.Setenv("ROOT_QUEUE_ID", "ca-root-cleanup-queue")

	ctx := context.Background()
	cfg, err := LoadConfig(ctx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify derived and default values
	if cfg.ProjectID != "test-project" {
		t.Errorf("expected ProjectID 'test-project', got '%s'", cfg.ProjectID)
	}
	if cfg.Location != "us-central1" {
		t.Errorf("expected Location 'us-central1', got '%s'", cfg.Location)
	}
	if cfg.CAPoolName != "test-pool" {
		t.Errorf("expected CAPoolName 'test-pool', got '%s'", cfg.CAPoolName)
	}
	// RootCAName is auto-discovered by the Rotator at runtime and is
	// intentionally empty right after config loading.
	if cfg.RootCAName != "" {
		t.Errorf("expected RootCAName to be empty (auto-discovered at runtime), got '%s'", cfg.RootCAName)
	}
	if cfg.RootCAPool != "root-pool" {
		t.Errorf("expected RootCAPool 'root-pool', got '%s'", cfg.RootCAPool)
	}
	if cfg.RootCALocation != "us-central1" {
		t.Errorf("expected RootCALocation 'us-central1', got '%s'", cfg.RootCALocation)
	}
	if cfg.StagingBuffer != 2*time.Hour {
		t.Errorf("expected StagingBuffer 2h, got %v", cfg.StagingBuffer)
	}
	if cfg.CleanupDelay != 48*time.Hour {
		t.Errorf("expected CleanupDelay 48h, got %v", cfg.CleanupDelay)
	}
	if cfg.CleanupJobName != "ca-cleanup-job" {
		t.Errorf("expected CleanupJobName 'ca-cleanup-job', got '%s'", cfg.CleanupJobName)
	}
	if cfg.QueueID != "ca-rotation-queue" {
		t.Errorf("expected QueueID 'ca-rotation-queue', got '%s'", cfg.QueueID)
	}
	if cfg.QueueLocation != "us-central1" {
		t.Errorf("expected QueueLocation 'us-central1', got '%s'", cfg.QueueLocation)
	}
	if cfg.CACount != 1 {
		t.Errorf("expected default CACount 1, got %d", cfg.CACount)
	}
}

func TestLoadConfig_Overrides(t *testing.T) {
	t.Setenv("PROJECT_ID", "test-project")
	t.Setenv("LOCATION", "us-central1")
	t.Setenv("POOL_NAME", "test-pool")
	t.Setenv("ROOT_CA_NAME", "test-root-ca")
	t.Setenv("ROOT_CA_POOL", "root-pool")
	t.Setenv("ROOT_CA_LOCATION", "us-central1")

	// Override defaults
	t.Setenv("STAGING_BUFFER", "5h")
	t.Setenv("CLEANUP_DELAY", "72h")
	t.Setenv("CLEANUP_JOB_NAME", "custom-cleanup")
	t.Setenv("QUEUE_ID", "custom-queue")
	t.Setenv("ROOT_QUEUE_ID", "custom-root-queue")
	t.Setenv("QUEUE_LOCATION", "europe-west1")
	t.Setenv("CA_COUNT", "3")

	ctx := context.Background()
	cfg, err := LoadConfig(ctx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.StagingBuffer != 5*time.Hour {
		t.Errorf("expected StagingBuffer 5h, got %v", cfg.StagingBuffer)
	}
	if cfg.CleanupDelay != 72*time.Hour {
		t.Errorf("expected CleanupDelay 72h, got %v", cfg.CleanupDelay)
	}
	if cfg.CleanupJobName != "custom-cleanup" {
		t.Errorf("expected CleanupJobName 'custom-cleanup', got '%s'", cfg.CleanupJobName)
	}
	if cfg.QueueID != "custom-queue" {
		t.Errorf("expected QueueID 'custom-queue', got '%s'", cfg.QueueID)
	}
	if cfg.QueueLocation != "europe-west1" {
		t.Errorf("expected QueueLocation 'europe-west1', got '%s'", cfg.QueueLocation)
	}
	if cfg.CACount != 3 {
		t.Errorf("expected CACount 3, got %d", cfg.CACount)
	}
}

func TestLoadConfig_InvalidCACount(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "Not a number", value: "three"},
		{name: "Zero", value: "0"},
		{name: "Negative", value: "-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			t.Setenv("PROJECT_ID", "test-project")
			t.Setenv("LOCATION", "us-central1")
			t.Setenv("POOL_NAME", "test-pool")
			t.Setenv("ROOT_CA_POOL", "root-pool")
			t.Setenv("ROOT_CA_LOCATION", "us-central1")
			t.Setenv("QUEUE_ID", "ca-rotation-queue")
			t.Setenv("CA_COUNT", tt.value)

			ctx := context.Background()
			_, err := LoadConfig(ctx)
			if err == nil {
				t.Fatalf("expected error for CA_COUNT=%q, got nil", tt.value)
			}
		})
	}
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	tests := []struct {
		name     string
		setupEnv func(t *testing.T)
	}{
		{
			name: "Missing LOCATION",
			setupEnv: func(t *testing.T) {
				t.Setenv("PROJECT_ID", "test-project")
				t.Setenv("POOL_NAME", "test-pool")
				t.Setenv("ROOT_CA_NAME", "test-root-ca")
				t.Setenv("ROOT_CA_POOL", "root-pool")
				t.Setenv("ROOT_CA_LOCATION", "us-central1")
			},
		},
		{
			name: "Missing POOL_NAME",
			setupEnv: func(t *testing.T) {
				t.Setenv("PROJECT_ID", "test-project")
				t.Setenv("LOCATION", "us-central1")
				t.Setenv("ROOT_CA_NAME", "test-root-ca")
				t.Setenv("ROOT_CA_POOL", "root-pool")
				t.Setenv("ROOT_CA_LOCATION", "us-central1")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv() // Ensure clean state
			tt.setupEnv(t)

			ctx := context.Background()
			_, err := LoadConfig(ctx)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
		})
	}
}
