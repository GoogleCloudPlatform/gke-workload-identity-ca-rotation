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
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	privateca "cloud.google.com/go/security/privateca/apiv1"
	"cloud.google.com/go/security/privateca/apiv1/privatecapb"
	"github.com/GoogleCloudPlatform/gke-workload-identity-ca-rotation/pkg/config"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Rotator struct {
	caClient   *privateca.CertificateAuthorityClient
	taskClient *cloudtasks.Client
	cfg        *config.Config
}

func NewRotator(ctx context.Context, cfg *config.Config) (*Rotator, error) {
	caClient, err := privateca.NewCertificateAuthorityClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create privateca client: %v", err)
	}

	taskClient, err := cloudtasks.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloudtasks client: %v", err)
	}

	return &Rotator{
		caClient:   caClient,
		taskClient: taskClient,
		cfg:        cfg,
	}, nil
}

func (r *Rotator) Close() error {
	r.caClient.Close()
	r.taskClient.Close()
	return nil
}

// RotateSubordinate orchestrates the zero-downtime rolling rotation of a Subordinate CA Pool.
// It iterates through all currently active CAs and sequentially replaces them one by one.
// An entirely empty pool is bootstrapped with CACount initial CAs. For a pool that already
// has at least one CA, the active count is compared against CACount; a mismatch logs an
// alert line (picked up by the log-match alert policy in terraform/monitoring.tf) but does
// not block the rotation, which operates on the CAs actually present in the pool - the
// rotator never creates or deletes CAs to fix drift on an already-seeded pool.
func (r *Rotator) RotateSubordinate(ctx context.Context) error {
	log.Printf("Starting Subordinate CA Rotation for pool: %s in %s", r.cfg.CAPoolName, r.cfg.Location)

	// --- Step 0: Auto-Discover Active Root CA ---
	rootPoolPath := fmt.Sprintf("projects/%s/locations/%s/caPools/%s", r.cfg.ProjectID, r.cfg.RootCALocation, r.cfg.RootCAPool)
	itRoot := r.caClient.ListCertificateAuthorities(ctx, &privatecapb.ListCertificateAuthoritiesRequest{
		Parent: rootPoolPath,
	})

	var activeRootCAName string
	for {
		ca, err := itRoot.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to list root CAs: %v", err)
		}
		if ca.State == privatecapb.CertificateAuthority_ENABLED {
			parts := strings.Split(ca.Name, "/")
			activeRootCAName = parts[len(parts)-1]
			break // Since we only allow one active root CA, we can stop searching.
		}
	}

	if activeRootCAName == "" {
		return fmt.Errorf("no enabled Root CA found in pool %s", rootPoolPath)
	}
	r.cfg.RootCAName = activeRootCAName
	log.Printf("Auto-discovered active Root CA: %s", r.cfg.RootCAName)

	parentPool := fmt.Sprintf("projects/%s/locations/%s/caPools/%s", r.cfg.ProjectID, r.cfg.Location, r.cfg.CAPoolName)

	// --- Step 1: Identify the Current State (Discovery) ---
	it := r.caClient.ListCertificateAuthorities(ctx, &privatecapb.ListCertificateAuthoritiesRequest{
		Parent: parentPool,
	})

	var activeCAs []*privatecapb.CertificateAuthority
	var stagedCA *privatecapb.CertificateAuthority
	var awaitingCA *privatecapb.CertificateAuthority
	var disabledCAs []*privatecapb.CertificateAuthority
	allCAsByID := make(map[string]*privatecapb.CertificateAuthority)

	for {
		ca, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to list CAs: %v", err)
		}

		// IDEMPOTENCY: Completely ignore CAs in the 30-day DELETED tombstone.
		// We only care about CAs that are active, staged, or disabled (awaiting cleanup).
		if ca.State == privatecapb.CertificateAuthority_DELETED {
			continue
		}

		parts := strings.Split(ca.Name, "/")
		caID := parts[len(parts)-1]
		allCAsByID[caID] = ca

		switch ca.State {
		case privatecapb.CertificateAuthority_ENABLED:
			activeCAs = append(activeCAs, ca)
		case privatecapb.CertificateAuthority_STAGED:
			stagedCA = ca
		case privatecapb.CertificateAuthority_AWAITING_USER_ACTIVATION:
			awaitingCA = ca
		case privatecapb.CertificateAuthority_DISABLED:
			disabledCAs = append(disabledCAs, ca)
		}
	}

	// --- Step 2: Idempotency Check & Self-Healing ---

	// Heal Stranded DISABLED CAs:
	// If we found CAs that are already disabled, it means a previous rotation
	// might have failed to schedule their cleanup. We reschedule it now as a safety measure.
	for _, strandedCA := range disabledCAs {
		log.Printf("Found stranded DISABLED CA: %s, ensuring cleanup is scheduled...", strandedCA.Name)
		if !r.cfg.DisableTaskScheduling {
			if err := r.ScheduleCleanup(ctx, strandedCA.Name); err != nil {
				return fmt.Errorf("failed to reschedule cleanup for %s: %v", strandedCA.Name, err)
			}
		}
	}

	// Fix Mid-Flip Crashes:
	// If a new CA is ENABLED but the old CA it replaces is ALSO still ENABLED, we crashed mid-flip.
	for _, activeCA := range activeCAs {
		if activeCA.Labels != nil {
			if replacedID, exists := activeCA.Labels["replaces"]; exists {
				if replacedCA, stillInPool := allCAsByID[replacedID]; stillInPool {
					if replacedCA.State == privatecapb.CertificateAuthority_ENABLED {
						log.Printf("Idempotency: Found mid-crash flip. %s was enabled but %s was not disabled. Fixing...", activeCA.Name, replacedCA.Name)
						_, err := r.caClient.DisableCertificateAuthority(ctx, &privatecapb.DisableCertificateAuthorityRequest{
							Name: replacedCA.Name,
						})
						if err == nil {
							replacedCA.State = privatecapb.CertificateAuthority_DISABLED
							if !r.cfg.DisableTaskScheduling {
								if err := r.ScheduleCleanup(ctx, replacedCA.Name); err != nil {
									log.Printf("Warning: failed to schedule cleanup for %s: %v", replacedCA.Name, err)
								}
							}
						} else {
							log.Printf("Warning: failed to disable %s: %v", replacedCA.Name, err)
						}
					}
				}
			}
		}
	}

	// --- Step 3: Bootstrap an empty pool, or compare the pool size against the
	// expected CA count ---
	// The ca_pool module creates Subordinate CA pools empty, so a pool with zero
	// CAs of any kind (active, staged, or awaiting activation) is bootstrapped here
	// with CACount initial CAs, cloning the baseline configuration (Config, Lifetime,
	// KeySpec) from the project's Root CA.
	//
	// For a pool that already has at least one CA, the rotator never creates or
	// deletes CAs to fix drift - changing the pool size is an operator decision.
	// A mismatch there only logs a stable "ALERT: CA count mismatch" line, which the
	// log-match alert policy in terraform/monitoring.tf turns into an incident
	// notification. The rolling rotation below proceeds over the CAs actually
	// present in the pool either way.
	if len(activeCAs) == 0 && stagedCA == nil && awaitingCA == nil {
		log.Printf("No CAs found in pool %s. Bootstrapping %d initial Subordinate CA(s)...",
			r.cfg.CAPoolName, r.cfg.CACount)

		rootPath := fmt.Sprintf("projects/%s/locations/%s/caPools/%s/certificateAuthorities/%s",
			r.cfg.ProjectID, r.cfg.RootCALocation, r.cfg.RootCAPool, r.cfg.RootCAName)
		rootCA, err := r.caClient.GetCertificateAuthority(ctx, &privatecapb.GetCertificateAuthorityRequest{
			Name: rootPath,
		})
		if err != nil {
			return fmt.Errorf("failed to fetch Root CA for bootstrapping: %v", err)
		}

		for i := 1; i <= r.cfg.CACount; i++ {
			if err := r.bootstrapSubordinate(ctx, parentPool, rootCA, i); err != nil {
				return fmt.Errorf("failed to bootstrap Subordinate CA %d/%d: %v", i, r.cfg.CACount, err)
			}
		}

		log.Printf("Successfully bootstrapped pool %s with %d Subordinate CA(s)", r.cfg.CAPoolName, r.cfg.CACount)
		return nil
	}

	enabledCAs, err := r.listEnabledCAs(ctx, parentPool)
	if err != nil {
		return err
	}
	if len(enabledCAs) != r.cfg.CACount {
		log.Printf("ALERT: CA count mismatch for pool %s: found %d active CA(s), but CA_COUNT is configured as %d - reconcile the pool manually or update ca_count in terraform",
			r.cfg.CAPoolName, len(enabledCAs), r.cfg.CACount)
	}

	// Filter out any active CAs that were already rotated in this cycle.
	var filteredActiveCAs []*privatecapb.CertificateAuthority
	for _, activeCA := range activeCAs {
		// IDEMPOTENCY (Crash DURING Flip): If the CA was just disabled by the "Mid-Flip Crash" fix above,
		// skip it so we don't try to rotate an already-processed old CA.
		if activeCA.State != privatecapb.CertificateAuthority_ENABLED {
			continue
		}

		// IDEMPOTENCY (Post-Flip Memory): If an active CA has a 'replaces' label pointing to a CA
		// that is STILL in the pool (e.g. within 48h), skip it because it was recently rotated.
		if activeCA.Labels != nil {
			if replacedID, exists := activeCA.Labels["replaces"]; exists {
				if _, stillInPool := allCAsByID[replacedID]; stillInPool {
					log.Printf("Idempotency: Skipping recently rotated CA %s (it replaces %s)", activeCA.Name, replacedID)
					continue
				}
			}
		}
		filteredActiveCAs = append(filteredActiveCAs, activeCA)
	}
	activeCAs = filteredActiveCAs

	// Sort active CAs by creation time (oldest first) so we rotate sequentially
	sort.Slice(activeCAs, func(i, j int) bool {
		return activeCAs[i].CreateTime.AsTime().Before(activeCAs[j].CreateTime.AsTime())
	})

	// If a CA was created but the job crashed before activating it, activate it now.
	if stagedCA == nil && awaitingCA != nil {
		log.Printf("A rotation is already in progress (AWAITING CA: %s), activating it...", awaitingCA.Name)
		var err error
		stagedCA, err = r.activateSubordinate(ctx, awaitingCA.Name)
		if err != nil {
			return err
		}
	}

	if stagedCA != nil {
		log.Printf("A rotation is already in progress (STAGED CA: %s), resuming...", stagedCA.Name)
		var oldestCA *privatecapb.CertificateAuthority
		if len(activeCAs) > 0 {
			// IDEMPOTENCY (Crash BEFORE Flip): POP the oldest CA from the active list because
			// we are about to finish its rotation using the leftover staged CA.
			// This prevents the loop later from rotating this same CA a second time.
			oldestCA = activeCAs[0]
			activeCAs = activeCAs[1:]
		}
		if err := r.FlipSubordinate(ctx, stagedCA, oldestCA); err != nil {
			return err
		}
	}

	// --- Step 3: Rolling Update Loop ---
	// Iterate through the remaining active CAs and rotate them sequentially.
	for i, oldCA := range activeCAs {
		log.Printf("Rolling rotation %d/%d: replacing CA %s", i+1, len(activeCAs), oldCA.Name)
		if err := r.rotateSingleSubordinate(ctx, parentPool, oldCA); err != nil {
			return fmt.Errorf("failed to rotate CA %s: %v", oldCA.Name, err)
		}
	}

	log.Printf("Successfully completed rolling rotation for pool %s", r.cfg.CAPoolName)
	return nil
}

// listEnabledCAs lists the currently enabled Subordinate CAs in a pool, oldest first.
func (r *Rotator) listEnabledCAs(ctx context.Context, parentPool string) ([]*privatecapb.CertificateAuthority, error) {
	it := r.caClient.ListCertificateAuthorities(ctx, &privatecapb.ListCertificateAuthoritiesRequest{
		Parent: parentPool,
	})

	var enabledCAs []*privatecapb.CertificateAuthority
	for {
		ca, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list CAs: %v", err)
		}
		if ca.State == privatecapb.CertificateAuthority_ENABLED {
			enabledCAs = append(enabledCAs, ca)
		}
	}

	sort.Slice(enabledCAs, func(i, j int) bool {
		return enabledCAs[i].CreateTime.AsTime().Before(enabledCAs[j].CreateTime.AsTime())
	})
	return enabledCAs, nil
}

// bootstrapSubordinate creates, activates, and enables a single initial Subordinate CA
// in an otherwise empty pool, cloning its baseline configuration (Config, Lifetime, KeySpec)
// from the project's Root CA.
func (r *Rotator) bootstrapSubordinate(ctx context.Context, parentPool string, rootCA *privatecapb.CertificateAuthority, index int) error {
	// Suffix the bootstrap index to guarantee unique CA IDs even if two CAs are
	// created within the same millisecond.
	caID := fmt.Sprintf("sub-ca-%d-%d", time.Now().UnixNano()/int64(time.Millisecond), index)
	newCAName := fmt.Sprintf("%s/certificateAuthorities/%s", parentPool, caID)

	createReq := &privatecapb.CreateCertificateAuthorityRequest{
		Parent:                 parentPool,
		CertificateAuthorityId: caID,
		CertificateAuthority: &privatecapb.CertificateAuthority{
			Type:     privatecapb.CertificateAuthority_SUBORDINATE,
			Config:   rootCA.Config,
			Lifetime: rootCA.Lifetime,
			KeySpec:  rootCA.KeySpec,
		},
	}

	log.Printf("Creating initial Subordinate CA: %s", newCAName)
	opCreate, err := r.caClient.CreateCertificateAuthority(ctx, createReq)
	if err != nil {
		return fmt.Errorf("failed to create CA: %v", err)
	}
	_, err = opCreate.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for CA creation: %v", err)
	}

	newCA, err := r.activateSubordinate(ctx, newCAName)
	if err != nil {
		return err
	}

	log.Printf("Enabling initial CA: %s", newCA.Name)
	opEnable, err := r.caClient.EnableCertificateAuthority(ctx, &privatecapb.EnableCertificateAuthorityRequest{
		Name: newCA.Name,
	})
	if err != nil {
		return fmt.Errorf("failed to enable initial CA: %v", err)
	}
	_, err = opEnable.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for initial CA enablement: %v", err)
	}

	return nil
}

func (r *Rotator) activateSubordinate(ctx context.Context, caName string) (*privatecapb.CertificateAuthority, error) {
	// 1. Fetch CSR from the awaiting CA
	log.Printf("Fetching CSR for CA: %s", caName)
	csrResp, err := r.caClient.FetchCertificateAuthorityCsr(ctx, &privatecapb.FetchCertificateAuthorityCsrRequest{
		Name: caName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch CSR: %v", err)
	}

	// 2. Request Root CA to sign the CSR
	log.Printf("Requesting Root CA to sign the CSR")
	rootPoolName := fmt.Sprintf("projects/%s/locations/%s/caPools/%s", r.cfg.ProjectID, r.cfg.RootCALocation, r.cfg.RootCAPool)
	certID := fmt.Sprintf("cert-%d", time.Now().UnixNano()/int64(time.Millisecond))
	createCertReq := &privatecapb.CreateCertificateRequest{
		Parent:                        rootPoolName,
		CertificateId:                 certID,
		IssuingCertificateAuthorityId: r.cfg.RootCAName,
		Certificate: &privatecapb.Certificate{
			CertificateConfig: &privatecapb.Certificate_PemCsr{
				PemCsr: csrResp.PemCsr,
			},
			Lifetime: durationpb.New(10 * 365 * 24 * time.Hour),
		},
	}
	certResp, err := r.caClient.CreateCertificate(ctx, createCertReq)
	if err != nil {
		return nil, fmt.Errorf("failed to have Root CA sign the CSR: %v", err)
	}

	// 3. Activate the new CA using the signed certificate
	log.Printf("Activating the Subordinate CA with the signed certificate")
	rootCAName := fmt.Sprintf("%s/certificateAuthorities/%s", rootPoolName, r.cfg.RootCAName)
	opActivate, err := r.caClient.ActivateCertificateAuthority(ctx, &privatecapb.ActivateCertificateAuthorityRequest{
		Name:             caName,
		PemCaCertificate: certResp.PemCertificate,
		SubordinateConfig: &privatecapb.SubordinateConfig{
			SubordinateConfig: &privatecapb.SubordinateConfig_CertificateAuthority{
				CertificateAuthority: rootCAName,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send activation request: %v", err)
	}

	activatedCA, err := opActivate.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for CA activation: %v", err)
	}

	return activatedCA, nil
}

// rotateSingleSubordinate handles the creation and flip for a single CA in the pool.
func (r *Rotator) rotateSingleSubordinate(ctx context.Context, parentPool string, oldCA *privatecapb.CertificateAuthority) error {
	caID := fmt.Sprintf("sub-ca-%d", time.Now().UnixNano()/int64(time.Millisecond))
	newCAName := fmt.Sprintf("%s/certificateAuthorities/%s", parentPool, caID)

	parts := strings.Split(oldCA.Name, "/")
	oldCAID := parts[len(parts)-1]

	log.Printf("Creating new Subordinate CA: %s (replaces %s)", newCAName, oldCAID)
	createReq := &privatecapb.CreateCertificateAuthorityRequest{
		Parent:                 parentPool,
		CertificateAuthorityId: caID,
		CertificateAuthority: &privatecapb.CertificateAuthority{
			Type: privatecapb.CertificateAuthority_SUBORDINATE,
			Config: &privatecapb.CertificateConfig{
				SubjectConfig: oldCA.Config.SubjectConfig,
				X509Config:    oldCA.Config.X509Config,
			},
			Lifetime: oldCA.Lifetime,
			KeySpec:  oldCA.KeySpec,
			Labels:   map[string]string{"replaces": oldCAID},
		},
	}

	opCreate, err := r.caClient.CreateCertificateAuthority(ctx, createReq)
	if err != nil {
		return fmt.Errorf("failed to create CA: %v", err)
	}
	_, err = opCreate.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for CA creation: %v", err)
	}

	newCA, err := r.activateSubordinate(ctx, newCAName)
	if err != nil {
		return err
	}

	return r.FlipSubordinate(ctx, newCA, oldCA)
}

// FlipSubordinate handles the delicate state transition to ensure zero downtime.
func (r *Rotator) FlipSubordinate(ctx context.Context, newCA, oldCA *privatecapb.CertificateAuthority) error {
	log.Printf("Enabling new CA: %s", newCA.Name)
	opEnable, err := r.caClient.EnableCertificateAuthority(ctx, &privatecapb.EnableCertificateAuthorityRequest{
		Name: newCA.Name,
	})
	if err != nil {
		return fmt.Errorf("failed to enable new CA: %v", err)
	}
	_, err = opEnable.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for CA enablement: %v", err)
	}

	if oldCA != nil {
		log.Printf("Disabling old CA: %s", oldCA.Name)
		opDisable, err := r.caClient.DisableCertificateAuthority(ctx, &privatecapb.DisableCertificateAuthorityRequest{
			Name: oldCA.Name,
		})
		if err != nil {
			return fmt.Errorf("failed to disable old CA: %v", err)
		}
		_, err = opDisable.Wait(ctx)
		if err != nil {
			return fmt.Errorf("failed to wait for CA disablement: %v", err)
		}

		if r.cfg.DisableTaskScheduling {
			log.Printf("Skipping Cloud Task scheduling because DISABLE_TASK_SCHEDULING is set.")
			return nil
		}

		// Schedule Cleanup via Cloud Tasks (using CleanupDelayHours = 48)
		return r.ScheduleCleanup(ctx, oldCA.Name)
	}

	return nil
}

func (r *Rotator) ScheduleCleanup(ctx context.Context, caName string) error {
	// Ensure we use the full resource name for the API call.
	if !strings.HasPrefix(caName, "projects/") {
		caName = fmt.Sprintf("projects/%s/locations/%s/caPools/%s/certificateAuthorities/%s",
			r.cfg.ProjectID, r.cfg.Location, r.cfg.CAPoolName, caName)
	}

	log.Printf("Scheduling cleanup for CA: %s in %.0f minutes", caName, r.cfg.CleanupDelay.Minutes())

	queuePath := fmt.Sprintf("projects/%s/locations/%s/queues/%s", r.cfg.ProjectID, r.cfg.QueueLocation, r.cfg.QueueID)
	jobURL := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/jobs/%s:run",
		r.cfg.ProjectID, r.cfg.Location, r.cfg.CleanupJobName)

	payload := map[string]interface{}{
		"overrides": map[string]interface{}{
			"containerOverrides": []map[string]interface{}{
				{
					"env": []map[string]string{
						{"name": "CA_TO_DELETE", "value": caName},
						{"name": "OPERATION", "value": "CLEANUP"},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(payload)

	// Extract CA ID for a shorter task name
	parts := strings.Split(caName, "/")
	caID := parts[len(parts)-1]
	taskID := fmt.Sprintf("cleanup-%s-%d", caID, time.Now().Unix())

	req := &cloudtaskspb.CreateTaskRequest{
		Parent: queuePath,
		Task: &cloudtaskspb.Task{
			Name: fmt.Sprintf("%s/tasks/%s", queuePath, taskID),
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{
					HttpMethod: cloudtaskspb.HttpMethod_POST,
					Url:        jobURL,
					Body:       body,
					Headers: map[string]string{
						"Content-Type": "application/json",
					},
					AuthorizationHeader: &cloudtaskspb.HttpRequest_OauthToken{
						OauthToken: &cloudtaskspb.OAuthToken{
							// ServiceAccountEmail is used by Cloud Tasks to generate an OAuth token
							// that has permission to trigger the Cloud Run API.
							ServiceAccountEmail: r.cfg.ServiceAccountEmail,
							Scope:               "https://www.googleapis.com/auth/cloud-platform",
						},
					},
				},
			},
			ScheduleTime: timestamppb.New(time.Now().Add(r.cfg.CleanupDelay)),
		},
	}

	_, err := r.taskClient.CreateTask(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create cloud task: %v", err)
	}

	log.Printf("Cleanup task scheduled successfully")
	return nil
}
