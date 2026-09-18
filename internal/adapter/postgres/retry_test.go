package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

func TestTransientClassification(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		err  error
		want bool
	}{
		"serialization failure": {app.ErrSerializationFailure, true},
		"version mismatch":      {app.ErrVersionMismatch, true},
		// A refusal does not improve on a second attempt, and repeating it
		// would turn a rejection into a wait.
		"insufficient funds": {&domain.Error{Code: domain.CodeInsufficientFunds}, false},
		"conflict":           {app.NewConflict("wallets_pkey"), false},
		"not found":          {app.ErrNotFound, false},
		"invariant violated": {app.NewInvariantViolation("ledger_arithmetic"), false},
		"anything else":      {errors.New("boom"), false},
		"wrapped transient":  {errWrap(app.ErrSerializationFailure), true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := transient(tc.err); got != tc.want {
				t.Fatalf("transient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func errWrap(err error) error { return errors.Join(errors.New("context"), err) }

func TestBackoffGrowsAndIsJittered(t *testing.T) {
	t.Parallel()
	policy := RetryPolicy{MaxAttempts: 5, BaseBackoff: 20 * time.Millisecond}

	// The jitter is the point of this test. Without it, writers that collided
	// together sleep the same amount and collide again at the same instant --
	// the backoff would synchronise exactly what it is meant to spread out.
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		start := time.Now()
		if err := backoff(context.Background(), policy, 1); err != nil {
			t.Fatal(err)
		}
		seen[time.Since(start).Round(time.Millisecond)] = true
	}
	if len(seen) < 3 {
		t.Fatalf("50 waits produced %d distinct durations; the backoff is not jittered", len(seen))
	}

	// And it grows: a later attempt waits longer than the first can.
	first := time.Now()
	_ = backoff(context.Background(), policy, 1)
	firstWait := time.Since(first)

	later := time.Now()
	_ = backoff(context.Background(), policy, 4)
	laterWait := time.Since(later)

	if laterWait <= firstWait {
		t.Errorf("attempt 4 waited %v, attempt 1 waited %v", laterWait, firstWait)
	}
}

func TestBackoffStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	// A retry that keeps sleeping after the client hung up holds a connection
	// for nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := backoff(ctx, RetryPolicy{BaseBackoff: time.Hour}, 3)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRetryPolicyFallsBackToSaneValues(t *testing.T) {
	t.Parallel()
	// A zero policy is what a caller gets from an empty struct, and an
	// unbounded or zero-wait retry is worse than no retry at all.
	got := RetryPolicy{}.normalised()
	if got.MaxAttempts < 1 || got.BaseBackoff <= 0 {
		t.Fatalf("normalised zero policy = %+v", got)
	}
}
