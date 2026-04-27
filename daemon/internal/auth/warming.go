package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	workflowbuilder "github.com/dhiyaan/deconstruct/daemon/internal/workflow"
)

type WarmingStore interface {
	workflowbuilder.Store
	VaultStore
	ListAuthProfiles(context.Context, string, int) ([]store.AuthProfile, error)
	GetAuthProfile(context.Context, string) (store.AuthProfile, error)
	SaveAuthProfile(context.Context, store.AuthProfile) error
	GetWorkflow(context.Context, string) (store.Workflow, error)
}

type WarmingRunner struct {
	store    *store.Store
	blobs    *blobstore.Store
	vault    *Vault
	lastRuns map[string]time.Time
}

type WarmingResult struct {
	ProfileID  string             `json:"profile_id"`
	WorkflowID string             `json:"workflow_id,omitempty"`
	Ran        bool               `json:"ran"`
	Run        *store.WorkflowRun `json:"run,omitempty"`
	Error      string             `json:"error,omitempty"`
}

func NewWarmingRunner(store *store.Store, blobs *blobstore.Store, vault *Vault) *WarmingRunner {
	return &WarmingRunner{store: store, blobs: blobs, vault: vault, lastRuns: map[string]time.Time{}}
}

func (r *WarmingRunner) DueProfiles(ctx context.Context, now time.Time) ([]store.AuthProfile, error) {
	profiles, err := r.store.ListAuthProfiles(ctx, "", 500)
	if err != nil {
		return nil, err
	}
	var due []store.AuthProfile
	for _, profile := range profiles {
		if !r.profileDue(profile, now) {
			continue
		}
		due = append(due, profile)
	}
	return due, nil
}

func (r *WarmingRunner) RunDue(ctx context.Context, now time.Time) ([]WarmingResult, error) {
	profiles, err := r.DueProfiles(ctx, now)
	if err != nil {
		return nil, err
	}
	results := make([]WarmingResult, 0, len(profiles))
	for _, profile := range profiles {
		result := r.RunProfile(ctx, profile, now)
		results = append(results, result)
	}
	return results, nil
}

func (r *WarmingRunner) RunProfile(ctx context.Context, profile store.AuthProfile, now time.Time) WarmingResult {
	result := WarmingResult{ProfileID: profile.ID}
	if profile.CookieWarming == nil || !profile.CookieWarming.Enabled {
		result.Error = "cookie warming is disabled"
		return result
	}
	result.WorkflowID = profile.CookieWarming.WorkflowID
	if result.WorkflowID == "" {
		result.Error = "cookie warming workflow is not configured"
		return result
	}
	if profile.SecretBundleID == "" {
		if profile.InteractiveLoginRequired {
			result.Error = "interactive login required"
		} else {
			result.Error = "auth profile has no secret bundle"
		}
		return result
	}
	if r.vault == nil {
		result.Error = "auth vault is not configured"
		return result
	}
	material, err := r.vault.Open(ctx, profile.SecretBundleID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	workflow, err := r.store.GetWorkflow(ctx, result.WorkflowID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	run, err := workflowbuilder.NewBuilder(r.store, r.blobs).Run(ctx, workflow, workflowbuilder.RunParams{
		WorkflowID:    workflow.ID,
		AuthProfileID: profile.ID,
		AuthMaterial:  material,
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Ran = true
	result.Run = &run
	r.lastRuns[profile.ID] = now
	if !run.OK {
		result.Error = run.Error
	}
	return result
}

func (r *WarmingRunner) profileDue(profile store.AuthProfile, now time.Time) bool {
	if profile.CookieWarming == nil || !profile.CookieWarming.Enabled || profile.CookieWarming.WorkflowID == "" {
		return false
	}
	interval := time.Duration(profile.CookieWarming.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	last, ok := r.lastRuns[profile.ID]
	return !ok || now.Sub(last) >= interval
}

func (r *WarmingRunner) MarkRan(profileID string, at time.Time) error {
	if profileID == "" {
		return fmt.Errorf("profile id is required")
	}
	r.lastRuns[profileID] = at
	return nil
}
