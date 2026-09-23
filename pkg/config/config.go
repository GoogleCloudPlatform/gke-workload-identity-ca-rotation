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
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/oauth2/google"
)

type Config struct {
	ProjectID             string
	Location              string
	CAPoolName            string
	RootCAName            string // Auto-discovered at runtime by Rotator
	RootCAPool            string
	RootCALocation        string
	RootCASubjectCN       string
	RootCASubjectOrg      string
	RootCAAlgorithm       string
	StagingBuffer         time.Duration
	CleanupDelay          time.Duration
	RootCleanupDelay      time.Duration
	RotatorJobName        string
	CleanupJobName        string
	RootCleanupJobName    string
	QueueID               string
	RootQueueID           string
	QueueLocation         string
	DisableTaskScheduling bool
	// ServiceAccountEmail is used by Cloud Tasks to generate an OIDC token
	// that has permission to trigger the Cloud Run Cleanup Job.
	ServiceAccountEmail string

	// CACount is the expected number of active Subordinate CAs per pool. An
	// entirely empty pool is bootstrapped with this many CAs. For a pool that
	// already has at least one CA, the rotator compares the active count against
	// this value on every run and logs an alert on mismatch instead of creating
	// or deleting CAs.
	CACount int
}

func LoadConfig(ctx context.Context) (*Config, error) {
	projectID := os.Getenv("PROJECT_ID")
	if projectID == "" {
		creds, err := google.FindDefaultCredentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("could not find default credentials to infer project ID: %v", err)
		}
		if creds.ProjectID == "" {
			return nil, fmt.Errorf("PROJECT_ID environment variable is required and could not be inferred from credentials")
		}
		projectID = creds.ProjectID
	}

	location := os.Getenv("LOCATION")
	if location == "" {
		return nil, fmt.Errorf("LOCATION environment variable is required")
	}

	poolName := os.Getenv("POOL_NAME")
	if poolName == "" {
		return nil, fmt.Errorf("POOL_NAME environment variable is required")
	}

	// CA_COUNT defines the expected number of active Subordinate CAs per pool.
	// An empty pool is bootstrapped with this many CAs; a non-empty pool is
	// compared against it on every run and logs an alert on mismatch. Defaults
	// to 1.
	caCount := 1
	if countStr := os.Getenv("CA_COUNT"); countStr != "" {
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return nil, fmt.Errorf("invalid CA_COUNT %q: %v", countStr, err)
		}
		if count < 1 {
			return nil, fmt.Errorf("CA_COUNT must be >= 1, got %d", count)
		}
		caCount = count
	}

	rootCAPool := os.Getenv("ROOT_CA_POOL")
	if rootCAPool == "" {
		return nil, fmt.Errorf("ROOT_CA_POOL environment variable is required")
	}

	rootCALocation := os.Getenv("ROOT_CA_LOCATION")
	if rootCALocation == "" {
		return nil, fmt.Errorf("ROOT_CA_LOCATION environment variable is required")
	}

	rootCASubjectCN := os.Getenv("ROOT_CA_SUBJECT_CN")
	rootCASubjectOrg := os.Getenv("ROOT_CA_SUBJECT_ORG")
	rootCAAlgorithm := os.Getenv("ROOT_CA_ALGORITHM")
	if rootCAAlgorithm == "" {
		rootCAAlgorithm = "RSA_PKCS1_4096_SHA256"
	}

	stagingBufferStr := os.Getenv("STAGING_BUFFER")
	if stagingBufferStr == "" {
		stagingBufferStr = "2h" // Default for Root CA rotation trust bundle propagation
	}
	stagingBuffer, err := time.ParseDuration(stagingBufferStr)
	if err != nil {
		return nil, fmt.Errorf("invalid STAGING_BUFFER: %v", err)
	}

	cleanupDelayStr := os.Getenv("CLEANUP_DELAY")
	if cleanupDelayStr == "" {
		cleanupDelayStr = "48h" // Default for cleanup tasks as per design
	}
	cleanupDelay, err := time.ParseDuration(cleanupDelayStr)
	if err != nil {
		return nil, fmt.Errorf("invalid CLEANUP_DELAY: %v", err)
	}

	rootCleanupDelayStr := os.Getenv("ROOT_CLEANUP_DELAY")
	if rootCleanupDelayStr == "" {
		rootCleanupDelayStr = "48h" // Default for root cleanup
	}
	rootCleanupDelay, err := time.ParseDuration(rootCleanupDelayStr)
	if err != nil {
		return nil, fmt.Errorf("invalid ROOT_CLEANUP_DELAY: %v", err)
	}

	rotatorJobName := os.Getenv("ROTATOR_JOB_NAME")
	if rotatorJobName == "" {
		rotatorJobName = "ca-rotator-job"
	}

	cleanupJobName := os.Getenv("CLEANUP_JOB_NAME")
	if cleanupJobName == "" {
		cleanupJobName = "ca-cleanup-job"
	}

	rootCleanupJobName := os.Getenv("ROOT_CLEANUP_JOB_NAME")
	if rootCleanupJobName == "" {
		rootCleanupJobName = "ca-root-cleanup-job"
	}

	queueID := os.Getenv("QUEUE_ID")
	if queueID == "" {
		return nil, fmt.Errorf("QUEUE_ID environment variable is required")
	}

	rootQueueID := os.Getenv("ROOT_QUEUE_ID")
	if rootQueueID == "" {
		return nil, fmt.Errorf("ROOT_QUEUE_ID environment variable is required")
	}

	queueLocation := os.Getenv("QUEUE_LOCATION")
	if queueLocation == "" {
		queueLocation = location
	}

	serviceAccountEmail := os.Getenv("SERVICE_ACCOUNT_EMAIL")
	if serviceAccountEmail == "" {
		serviceAccountEmail = fmt.Sprintf("ca-automation-sa@%s.iam.gserviceaccount.com", projectID)
	}

	disableTaskScheduling := os.Getenv("DISABLE_TASK_SCHEDULING") == "true"

	return &Config{
		ProjectID:             projectID,
		Location:              location,
		CAPoolName:            poolName,
		RootCAName:            "", // Auto-discovered at runtime
		RootCAPool:            rootCAPool,
		RootCALocation:        rootCALocation,
		RootCASubjectCN:       rootCASubjectCN,
		RootCASubjectOrg:      rootCASubjectOrg,
		RootCAAlgorithm:       rootCAAlgorithm,
		StagingBuffer:         stagingBuffer,
		CleanupDelay:          cleanupDelay,
		RootCleanupDelay:      rootCleanupDelay,
		RotatorJobName:        rotatorJobName,
		CleanupJobName:        cleanupJobName,
		RootCleanupJobName:    rootCleanupJobName,
		QueueID:               queueID,
		RootQueueID:           rootQueueID,
		QueueLocation:         queueLocation,
		DisableTaskScheduling: disableTaskScheduling,
		ServiceAccountEmail:   serviceAccountEmail,
		CACount:               caCount,
	}, nil
}
