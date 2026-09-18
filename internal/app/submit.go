package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// SubmitResult is what a provider gets back.
//
// The outcome is in Transaction.Status(), not in the error returned alongside
// it. A business rejection is a *recorded result*: it is committed with its
// failure code so a resend gets the same answer. Returning it as an error would
// make the first submission fail and its replay succeed, because a replay reads
// a stored row and has nothing to fail about. The status is the one place both
// paths agree.
//
// Execute's error is reserved for what is not a recorded outcome: malformed
// input, a conflict, an unsupported kind, or infrastructure.
type SubmitResult struct {
	Transaction domain.WagerTransaction

	// Balance is the balance observed when the operation was processed -- not
	// the wallet's balance now. On a replay those differ as soon as anything
	// else has touched the wallet, and the caller is owed the answer its
	// original request produced.
	Balance domain.Money

	// Replay says the operation had already been applied and this is its stored
	// outcome.
	Replay bool
}

// SubmitTransaction applies an operation from a provider, exactly once.
type SubmitTransaction struct {
	uow     UnitOfWork
	queries Queries
	ids     IDGenerator
	clock   Clock
}

func NewSubmitTransaction(uow UnitOfWork, queries Queries, ids IDGenerator, clock Clock) *SubmitTransaction {
	return &SubmitTransaction{uow: uow, queries: queries, ids: ids, clock: clock}
}

// Execute records and applies the operation.
//
// The shape is deliberate: look for an existing result, then try to write, then
// treat a uniqueness violation as the authoritative answer. The lookup is an
// optimisation -- most resends are resends -- and the constraint is the actual
// guarantee. Between a lookup and an insert, five more copies of the same
// request fit; all of them would find nothing and all of them would proceed.
// See docs/adr/0006-idempotency-hash.md.
func (uc *SubmitTransaction) Execute(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	parsed, err := uc.parse(cmd)
	if err != nil {
		return SubmitResult{}, err
	}

	// Fast path: an operation we have already seen.
	if existing, err := uc.queries.Transactions().FindByBusinessID(ctx, parsed.providerID, parsed.externalID); err == nil {
		return uc.replay(existing, parsed)
	} else if !errors.Is(err, ErrNotFound) {
		return SubmitResult{}, err
	}

	result, err := uc.apply(ctx, parsed)
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, ErrConflict) {
		return SubmitResult{}, err
	}

	// Someone else won the race. Which constraint refused decides what the
	// answer is, which is why the adapter names it.
	var conflict *ConstraintError
	if errors.As(err, &conflict) && conflict.Name == constraintIdempotencyKey {
		return SubmitResult{}, fmt.Errorf(
			"%w: the idempotency key belongs to another operation", ErrConflict)
	}

	existing, readErr := uc.queries.Transactions().FindByBusinessID(ctx, parsed.providerID, parsed.externalID)
	if readErr != nil {
		// The insert was refused for a uniqueness reason we cannot resolve into
		// a stored result. Reporting the original conflict is more honest than
		// inventing one.
		return SubmitResult{}, err
	}
	return uc.replay(existing, parsed)
}

// constraintIdempotencyKey is the index that catches a key reused for different
// content. The name has to match the schema, and a test in the HTTP adapter
// checks that every name it maps still exists in the embedded migration.
const constraintIdempotencyKey = "wager_transactions_idempotency_key"

// replay answers from a stored operation, after proving it is the same one.
func (uc *SubmitTransaction) replay(stored domain.WagerTransaction, parsed parsedSubmit) (SubmitResult, error) {
	// Same business identity, different content: the client reused
	// (provider, externalId) for something else.
	if stored.PayloadHash() != parsed.hash {
		return SubmitResult{}, fmt.Errorf(
			"%w: this operation was already submitted with different content", ErrConflict)
	}
	// Same operation under a different key. The pair identifies the operation,
	// so re-submitting it with a new key is not a new operation.
	if stored.IdempotencyKey() != parsed.key {
		return SubmitResult{}, fmt.Errorf(
			"%w: this operation was already submitted under another idempotency key", ErrConflict)
	}

	return SubmitResult{
		Transaction: stored,
		Balance:     stored.BalanceAfter(),
		Replay:      true,
	}, nil
}

type parsedSubmit struct {
	key        domain.IdempotencyKey
	hash       domain.PayloadHash
	providerID domain.ProviderID
	externalID domain.ExternalTransactionID
	playerID   domain.PlayerID
	walletID   domain.WalletID
	roundID    domain.RoundID
	gameID     domain.GameID
	kind       domain.Kind
	money      domain.Money
	reference  domain.ExternalTransactionID
}

func (uc *SubmitTransaction) parse(cmd SubmitCommand) (parsedSubmit, error) {
	var p parsedSubmit
	var err error

	// The hash is computed from the raw strings, before any of this. Hashing
	// parsed values would make the fingerprint depend on how lenient the parser
	// is on the day it runs.
	if p.hash, err = cmd.PayloadHash(); err != nil {
		return parsedSubmit{}, err
	}

	if p.key, err = domain.ParseIdempotencyKey(cmd.IdempotencyKey); err != nil {
		return parsedSubmit{}, err
	}
	if p.providerID, err = domain.ParseProviderID(cmd.ProviderID); err != nil {
		return parsedSubmit{}, err
	}
	if p.externalID, err = domain.ParseExternalTransactionID(cmd.ExternalID); err != nil {
		return parsedSubmit{}, err
	}
	if p.playerID, err = domain.ParsePlayerID(cmd.PlayerID); err != nil {
		return parsedSubmit{}, err
	}
	if p.walletID, err = domain.ParseWalletID(cmd.WalletID); err != nil {
		return parsedSubmit{}, err
	}
	if p.roundID, err = domain.ParseRoundID(cmd.RoundID); err != nil {
		return parsedSubmit{}, err
	}
	if p.gameID, err = domain.ParseGameID(cmd.GameID); err != nil {
		return parsedSubmit{}, err
	}

	p.kind = domain.Kind(cmd.Kind)
	if !p.kind.Valid() {
		return parsedSubmit{}, fmt.Errorf("unknown kind %q: %w", cmd.Kind, domain.ErrInvalidKind)
	}
	if p.kind.IsReversal() {
		// Reversals resolve a reference before they can be applied, and that
		// machinery does not exist yet. Saying so plainly beats accepting the
		// operation and quietly doing something else with it.
		return parsedSubmit{}, fmt.Errorf("%w: %s is not supported yet", ErrNotImplemented, p.kind)
	}

	currency, err := domain.ParseCurrency(cmd.Currency)
	if err != nil {
		return parsedSubmit{}, err
	}
	// External money is never negative, whatever the caller says.
	if p.money, err = domain.ParseExternalMoney(cmd.Amount, currency); err != nil {
		return parsedSubmit{}, err
	}

	if cmd.ReferenceExternalID != "" {
		if p.reference, err = domain.ParseExternalTransactionID(cmd.ReferenceExternalID); err != nil {
			return parsedSubmit{}, err
		}
	}
	return p, nil
}

// apply writes the operation and its effect in one transaction.
func (uc *SubmitTransaction) apply(ctx context.Context, p parsedSubmit) (SubmitResult, error) {
	transactionID, err := uc.ids.NewTransactionID(ctx)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("minting a transaction id: %w", err)
	}

	at := uc.clock.Now()
	transaction, err := domain.NewExternalTransaction(domain.ExternalTransactionParams{
		ID:                  transactionID,
		Kind:                p.kind,
		ProviderID:          p.providerID,
		ExternalID:          p.externalID,
		IdempotencyKey:      p.key,
		PayloadHash:         p.hash,
		WalletID:            p.walletID,
		PlayerID:            p.playerID,
		RoundID:             p.roundID,
		GameID:              p.gameID,
		Money:               p.money,
		ReferenceExternalID: p.reference,
		CreatedAt:           at,
	})
	if err != nil {
		return SubmitResult{}, err
	}

	var result SubmitResult
	err = uc.uow.Do(ctx, func(ctx context.Context, repos Repositories) error {
		// Locking, not just reading. Two bets on the same wallet queue here
		// instead of both deciding against the same balance -- and the read
		// happens inside the callback, so a retry sees fresh state rather than
		// re-deciding on the numbers that already lost.
		wallet, err := repos.Wallets().FindByIDForUpdate(ctx, p.walletID)
		if err != nil {
			return err
		}
		if wallet.PlayerID() != p.playerID {
			return fmt.Errorf("%w: the wallet does not belong to that player", ErrInvalidInput)
		}

		movement, moved, err := uc.movement(ctx, wallet, transaction, transactionID, at)
		if err != nil {
			rejected, rejectErr := uc.reject(transaction, err, at)
			if rejectErr != nil {
				// Not a business rejection: infrastructure, or a transition the
				// domain refused. Either way it rolls back and stays retryable.
				return rejectErr
			}
			if err := repos.Transactions().Insert(ctx, rejected); err != nil {
				return err
			}
			// Committing on purpose: the rejection *is* the result, and a
			// resend has to read it rather than try again.
			result = SubmitResult{Transaction: rejected, Balance: wallet.Balance()}
			return nil
		}

		applied, err := transaction.MarkProcessed(balanceAfter(wallet, movement, moved), at)
		if err != nil {
			return err
		}
		if err := repos.Transactions().Insert(ctx, applied); err != nil {
			return err
		}

		if !moved {
			// LOSS moves nothing: no ledger entry, no version bump, and the
			// balance reported is the one the wallet already had.
			result = SubmitResult{Transaction: applied, Balance: wallet.Balance()}
			return nil
		}

		if err := repos.Wallets().UpdateBalance(ctx, movement.Wallet, wallet.Version()); err != nil {
			return err
		}
		if err := repos.Ledger().Append(ctx, movement.Entry); err != nil {
			return err
		}
		result = SubmitResult{Transaction: applied, Balance: movement.Wallet.Balance()}
		return nil
	})
	if err != nil {
		return SubmitResult{}, err
	}
	return result, nil
}

func balanceAfter(wallet domain.Wallet, movement domain.Movement, moved bool) domain.Money {
	if moved {
		return movement.Wallet.Balance()
	}
	return wallet.Balance()
}

// movement applies the kind to the wallet. The second return says whether money
// actually moved, which is false for LOSS and only for LOSS.
func (uc *SubmitTransaction) movement(
	ctx context.Context,
	wallet domain.Wallet,
	transaction domain.WagerTransaction,
	transactionID domain.TransactionID,
	at time.Time,
) (domain.Movement, bool, error) {
	if !transaction.Kind().MovesMoney() {
		return domain.Movement{}, false, nil
	}

	entryID, err := uc.ids.NewLedgerEntryID(ctx)
	if err != nil {
		return domain.Movement{}, false, fmt.Errorf("minting a ledger entry id: %w", err)
	}

	var movement domain.Movement
	switch transaction.Kind() {
	case domain.KindBet:
		movement, err = wallet.Debit(entryID, transactionID, transaction.Money(), at)
	case domain.KindWin:
		movement, err = wallet.Credit(entryID, transactionID, transaction.Money(), at)
	default:
		err = fmt.Errorf("%w: %s", ErrNotImplemented, transaction.Kind())
	}
	if err != nil {
		return domain.Movement{}, false, err
	}
	return movement, true, nil
}

// reject turns a domain refusal into a recorded rejection.
//
// Only a domain rejection qualifies. An infrastructure failure is transient and
// has to stay retryable: storing it as terminal would turn "the database
// blinked" into "your bet was refused", permanently.
func (uc *SubmitTransaction) reject(transaction domain.WagerTransaction, cause error, at time.Time) (domain.WagerTransaction, error) {
	var de *domain.Error
	if !errors.As(cause, &de) {
		return domain.WagerTransaction{}, cause
	}
	return transaction.Reject(de.Code, at)
}
