package app

import (
	"context"
	"errors"

	"github.com/yvesas/wagering-core/internal/domain"
)

// resolutionOutcome is what looking for a reversal's reference concluded.
type resolutionOutcome int

const (
	// resolveWait: the reference is not usable yet but still might be. Not
	// there at all, or there and not finished.
	resolveWait resolutionOutcome = iota

	// resolveApply: found, compatible, not already reversed.
	resolveApply

	// resolveReject: it will never be usable. Waiting longer changes nothing.
	resolveReject
)

// resolution carries the conclusion and, when it applies, what to do with it.
type resolution struct {
	outcome   resolutionOutcome
	reference domain.WagerTransaction
	direction domain.Direction

	// code explains a rejection. It is the failure code the provider reads, so
	// it is part of the contract.
	code   domain.Code
	detail string
}

func rejectResolution(code domain.Code, detail string) resolution {
	return resolution{outcome: resolveReject, code: code, detail: detail}
}

// resolveReference decides what a reversal can do about the operation it names.
//
// It runs inside the transaction that will apply the reversal, and after the
// wallet row is locked. That matters for the "already reversed" check: two
// reversals of the same reference must agree on the wallet, so the wallet lock
// has already serialised them by the time this looks. The unique index on the
// resolved reference is the second guarantee, for the same reason constraints
// exist at all.
//
// The full table is in docs/adr/0008-reversals.md.
func resolveReference(
	ctx context.Context,
	repos Repositories,
	reversal domain.WagerTransaction,
) (resolution, error) {
	reference, err := repos.Transactions().FindByBusinessID(
		ctx, reversal.ProviderID(), reversal.ReferenceExternalID())
	switch {
	case errors.Is(err, ErrNotFound):
		// Out-of-order delivery is expected, not exceptional: the reversal may
		// simply have overtaken what it undoes.
		return resolution{outcome: resolveWait}, nil
	case err != nil:
		return resolution{}, err
	}

	switch reference.Status() {
	case domain.StatusPending, domain.StatusPendingReference:
		// Still in flight. Rejecting now would be deciding too early -- it can
		// still become PROCESSED.
		return resolution{outcome: resolveWait}, nil
	case domain.StatusRejected, domain.StatusFailed:
		// It never moved money, and it never will. Waiting would be waiting
		// forever.
		return rejectResolution(domain.CodeReferenceNotReversible,
			"the referenced operation ended as "+string(reference.Status())), nil
	}

	if !reversal.Kind().CanReverse(reference.Kind()) {
		return rejectResolution(domain.CodeReferenceNotReversible,
			string(reversal.Kind())+" cannot reverse "+string(reference.Kind())), nil
	}

	if mismatch := disagreement(reversal, reference); mismatch != "" {
		return rejectResolution(domain.CodeReferenceMismatch, mismatch), nil
	}

	// Partial reversals are out of scope, so anything but an exact match is a
	// different operation wearing this one's reference.
	if reversal.Money() != reference.Money() {
		return rejectResolution(domain.CodeReferenceAmountMismatch,
			"the reversal is for "+reversal.Money().String()+
				" and the reference is for "+reference.Money().String()), nil
	}

	existing, err := repos.Transactions().FindProcessedReversalOf(ctx, reference.ID())
	switch {
	case err == nil:
		// One successful reversal per operation, of any kind. A refund and then
		// a rollback of the same bet are different types and would return the
		// same money twice.
		return rejectResolution(domain.CodeAlreadyReversed,
			"already reversed by "+existing.ID().String()), nil
	case !errors.Is(err, ErrNotFound):
		return resolution{}, err
	}

	direction, err := domain.ReversalDirection(reference.Kind())
	if err != nil {
		return resolution{}, err
	}

	return resolution{
		outcome:   resolveApply,
		reference: reference,
		direction: direction,
	}, nil
}

// disagreement names the first field the two operations do not share, or "".
//
// A reversal has to be about the same money in the same place: a rollback that
// agreed on nothing but the reference id would move a different player's
// balance.
func disagreement(reversal, reference domain.WagerTransaction) string {
	switch {
	case reversal.PlayerID() != reference.PlayerID():
		return "the reversal and its reference name different players"
	case reversal.WalletID() != reference.WalletID():
		return "the reversal and its reference name different wallets"
	case reversal.Money().Currency() != reference.Money().Currency():
		return "the reversal and its reference are in different currencies"
	case reversal.RoundID() != reference.RoundID():
		return "the reversal and its reference belong to different rounds"
	default:
		// The provider is not compared here: the reference was looked up by
		// (provider, externalId), so it cannot belong to another one.
		return ""
	}
}
