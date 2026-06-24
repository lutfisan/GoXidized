package scheduler

import (
	"context"
	"errors"
	"time"
)

type WorkerLease struct {
	TargetID  string
	WorkerID  string
	JobID     string
	Now       time.Time
	ExpiresAt time.Time
}

type LeaseStore interface {
	TryAcquireLease(context.Context, WorkerLease) (bool, error)
	RenewLease(context.Context, WorkerLease) (bool, error)
	ReleaseLease(context.Context, WorkerLease) error
}

func (l WorkerLease) Validate() error {
	if l.TargetID == "" {
		return errors.New("lease target id is required")
	}
	if l.WorkerID == "" {
		return errors.New("lease worker id is required")
	}
	if l.JobID == "" {
		return errors.New("lease job id is required")
	}
	if l.Now.IsZero() {
		return errors.New("lease now is required")
	}
	if l.ExpiresAt.IsZero() || !l.ExpiresAt.After(l.Now) {
		return errors.New("lease expires_at must be after now")
	}
	return nil
}
