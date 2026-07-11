package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type CloudOperationKind string

const (
	CloudOperationSync      CloudOperationKind = "sync"
	CloudOperationReconnect CloudOperationKind = "reconnect"
)

type CloudOperationStatus struct {
	OperationID string             `json:"operationId"`
	JournalID   string             `json:"journalId"`
	Kind        CloudOperationKind `json:"kind"`
	State       string             `json:"state"`
	StartedAt   time.Time          `json:"startedAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	SafeMessage string             `json:"safeMessage"`
}

// CloudOperationRunner serializes mutating work per Journal. Joining an
// existing operation avoids competing lease/pointer changes from duplicate UI
// clicks while preserving cancellation at the operation boundary.
type CloudOperationRunner struct {
	mu         sync.Mutex
	operations map[string]*runningCloudOperation
}
type runningCloudOperation struct {
	status CloudOperationStatus
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCloudOperationRunner() *CloudOperationRunner {
	return &CloudOperationRunner{operations: map[string]*runningCloudOperation{}}
}
func (r *CloudOperationRunner) Start(ctx context.Context, journalID string, kind CloudOperationKind, fn func(context.Context) error) (CloudOperationStatus, bool) {
	r.mu.Lock()
	if existing := r.operations[journalID]; existing != nil {
		status := existing.status
		r.mu.Unlock()
		return status, false
	}
	operationCtx, cancel := context.WithCancel(ctx)
	now := time.Now().UTC()
	run := &runningCloudOperation{status: CloudOperationStatus{OperationID: uuid.NewString(), JournalID: journalID, Kind: kind, State: "running", StartedAt: now, UpdatedAt: now}, cancel: cancel, done: make(chan struct{})}
	r.operations[journalID] = run
	r.mu.Unlock()
	go func() {
		err := fn(operationCtx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if operationCtx.Err() != nil {
			run.status.State = "canceled"
			run.status.SafeMessage = "Operation canceled"
		} else if err != nil {
			run.status.State = "failed"
			run.status.SafeMessage = safeCloudError(err)
		} else {
			run.status.State = "succeeded"
			run.status.SafeMessage = "Completed"
		}
		run.status.UpdatedAt = time.Now().UTC()
		close(run.done)
		delete(r.operations, journalID)
	}()
	return run.status, true
}
func (r *CloudOperationRunner) Cancel(journalID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.operations[journalID]
	if run == nil {
		return false
	}
	run.status.State = "canceling"
	run.status.UpdatedAt = time.Now().UTC()
	run.cancel()
	return true
}
func (r *CloudOperationRunner) Status(journalID string) (CloudOperationStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.operations[journalID]
	if run == nil {
		return CloudOperationStatus{}, false
	}
	return run.status, true
}
func safeCloudError(err error) string {
	if isVault(err, VaultConflict) {
		return "Remote state changed; resolve the conflict before retrying."
	}
	if isVault(err, VaultUnauthorized) {
		return "Provider authentication failed."
	}
	if isVault(err, VaultUnavailable) {
		return "Provider is unavailable; local work was preserved."
	}
	return "Cloud operation failed; local work was preserved."
}
func RetryDelay(err error, attempt int) (time.Duration, bool) {
	if isVault(err, VaultConflict) || isVault(err, VaultUnauthorized) || isVault(err, VaultMalformed) {
		return 0, false
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Second * time.Duration(1<<attempt), true
}
func RequireOperationContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("operation context is required")
	}
	return ctx.Err()
}
