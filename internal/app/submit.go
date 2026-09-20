package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	waiting ReferencePolicy
	events  eventRecorder
	metrics OperationMetrics
	logger  *slog.Logger
}

func NewSubmitTransaction(uow UnitOfWork, queries Queries, ids IDGenerator, clock Clock, waiting ReferencePolicy, metrics OperationMetrics, logger *slog.Logger) *SubmitTransaction {
	if logger == nil {
		logger = slog.Default()
	}
	return &SubmitTransaction{
		uow:     uow,
		queries: queries,
		ids:     ids,
		clock:   clock,
		waiting: waiting.normalised(),
		events:  newEventRecorder(ids, clock),
		metrics: operationMetricsOr(metrics),
		logger:  logger,
	}
}

// Execute opens a transaction and applies the operation.
//
// This is the HTTP path. The queue path shares [SubmitTransaction.ExecuteIn]
// instead, because the inbox record has to land in the same commit -- see
// docs/adr/0009-inbox-and-queue.md. Both go through the same code below, so
// there is no queue version of the rules and no HTTP version of them.
func (uc *SubmitTransaction) Execute(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	started := time.Now()
	result, err := uc.execute(ctx, cmd)
	uc.Settled(ctx, SourceHTTP, time.Since(started), result, err)
	return result, err
}

// Settled reports one finished operation, to the meters and to the log.
//
// It is exported because the queue is an entry port too and reports the same
// things with a different source. The alternative -- reporting inside the code
// both ports share -- would need the source to travel down there, and the
// source is a property of the port, which is the one thing that shared code
// deliberately does not know.
//
// The elapsed time comes from time.Now and not from the Clock port. The clock
// exists so a business timestamp is decided by the caller rather than by the
// machine; a duration on a histogram is a measurement, and reading it from a
// movable test clock would make every observation zero.
func (uc *SubmitTransaction) Settled(ctx context.Context, source string, took time.Duration, result SubmitResult, err error) {
	uc.metrics.OperationLatency(source, took)

	if err != nil {
		// Contention that the retry could not absorb and that reached the
		// caller. The retries themselves are counted by the unit of work, so
		// the two together say how much of the contention was hidden.
		if errors.Is(err, ErrVersionMismatch) {
			uc.metrics.WalletContention()
		}
		return
	}

	// Kind and status are closed sets from the domain, which is what keeps this
	// label pair bounded. A rejection is an outcome like any other and is
	// counted here, not as an error: that is the whole point of recording the
	// refusal rather than returning it.
	transaction := result.Transaction
	uc.metrics.OperationSettled(source,
		string(transaction.Kind()), string(transaction.Status()))

	// The identifiers REQ-OBS-001 asks for, on the one line that has all of
	// them. What is *not* here is the amount and the balance: a log aggregator
	// is not where a financial payload belongs, and the transaction id is
	// enough to find both in the database for anyone entitled to see them.
	uc.logger.InfoContext(ctx, "operation settled",
		slog.String("source", source),
		slog.String("transactionId", transaction.ID().String()),
		slog.String("walletId", transaction.WalletID().String()),
		slog.String("playerId", transaction.PlayerID().String()),
		slog.String("providerId", transaction.ProviderID().String()),
		slog.String("externalTransactionId", transaction.ExternalID().String()),
		slog.String("kind", string(transaction.Kind())),
		slog.String("status", string(transaction.Status())),
		slog.String("failureCode", string(transaction.FailureCode())),
		slog.Bool("replay", result.Replay),
		slog.Duration("took", took),
		slog.String("correlationId", CorrelationIDFrom(ctx)))
}

func (uc *SubmitTransaction) execute(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	var result SubmitResult

	err := uc.uow.Do(ctx, func(ctx context.Context, repos Repositories) error {
		var err error
		result, err = uc.ExecuteIn(ctx, repos, cmd)
		return err
	})
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

	parsed, parseErr := uc.parse(cmd)
	if parseErr != nil {
		return SubmitResult{}, parseErr
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

// ExecuteIn applies the operation inside a transaction the caller owns.
//
// The lookup happens here rather than before the transaction, so it sees the
// same snapshot everything else in this commit sees. On the HTTP path that
// costs nothing; on the queue path it is what lets the inbox record and the
// financial effect be one commit.
func (uc *SubmitTransaction) ExecuteIn(ctx context.Context, repos Repositories, cmd SubmitCommand) (SubmitResult, error) {
	// Authorisation is here, in the code both ports share, and not in the HTTP
	// handler. Written at the edge it would be an HTTP rule, and the queue --
	// which reaches this same method -- would have none. See
	// docs/adr/0011-authentication-and-isolation.md.
	identity, err := caller(ctx, ScopeSubmit)
	if err != nil {
		return SubmitResult{}, err
	}

	parsed, err := uc.parse(cmd)
	if err != nil {
		return SubmitResult{}, err
	}
	if err := identity.mayActAs(parsed.providerID); err != nil {
		return SubmitResult{}, err
	}

	// An operation we have already applied: answer from what is stored.
	existing, err := repos.Transactions().FindByBusinessID(ctx, parsed.providerID, parsed.externalID)
	switch {
	case err == nil:
		return uc.replay(existing, parsed)
	case !errors.Is(err, ErrNotFound):
		return SubmitResult{}, err
	}

	return uc.apply(ctx, repos, parsed)
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

// apply writes the operation and its effect, inside the caller's transaction.
func (uc *SubmitTransaction) apply(ctx context.Context, repos Repositories, p parsedSubmit) (SubmitResult, error) {
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

	// Locking, not just reading. Two bets on the same wallet queue here instead
	// of both deciding against the same balance -- and the read happens inside
	// the transaction, so a retry sees fresh state rather than re-deciding on
	// the numbers that already lost.
	wallet, err := repos.Wallets().FindByIDForUpdate(ctx, p.walletID)
	if err != nil {
		return SubmitResult{}, err
	}
	if wallet.PlayerID() != p.playerID {
		return SubmitResult{}, fmt.Errorf("%w: the wallet does not belong to that player", ErrInvalidInput)
	}

	// A reversal has to find what it undoes before it can do anything, and the
	// lookup happens here -- after the wallet is locked -- so nothing can
	// reverse the same operation in between.
	if transaction.Kind().IsReversal() {
		return uc.resolve(ctx, repos, transaction, wallet, at)
	}

	movement, moved, err := uc.movement(ctx, wallet, transaction, transactionID, at)
	if err != nil {
		rejected, rejectErr := uc.reject(transaction, err, at)
		if rejectErr != nil {
			// Not a business rejection: infrastructure, or a transition the
			// domain refused. Either way it rolls back and stays retryable.
			return SubmitResult{}, rejectErr
		}
		if err := repos.Transactions().Insert(ctx, rejected); err != nil {
			return SubmitResult{}, err
		}
		if err := uc.emit(ctx, repos, rejected, wallet, domain.LedgerEntry{}, false); err != nil {
			return SubmitResult{}, err
		}
		// The rejection *is* the result, and a resend has to read it rather
		// than try again.
		return SubmitResult{Transaction: rejected, Balance: wallet.Balance()}, nil
	}

	applied, err := transaction.MarkProcessed(balanceAfter(wallet, movement, moved), at)
	if err != nil {
		return SubmitResult{}, err
	}
	if err := repos.Transactions().Insert(ctx, applied); err != nil {
		return SubmitResult{}, err
	}

	if !moved {
		// LOSS moves nothing: no ledger entry, no version bump, and the balance
		// reported is the one the wallet already had. It still reports that the
		// operation finished -- that is why "processed" and "balance changed"
		// are two events and not one.
		if err := uc.emit(ctx, repos, applied, wallet, domain.LedgerEntry{}, false); err != nil {
			return SubmitResult{}, err
		}
		return SubmitResult{Transaction: applied, Balance: wallet.Balance()}, nil
	}

	if err := repos.Wallets().UpdateBalance(ctx, movement.Wallet, wallet.Version()); err != nil {
		return SubmitResult{}, err
	}
	if err := repos.Ledger().Append(ctx, movement.Entry); err != nil {
		return SubmitResult{}, err
	}
	if err := uc.emit(ctx, repos, applied, movement.Wallet, movement.Entry, true); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{Transaction: applied, Balance: movement.Wallet.Balance()}, nil
}

// emit records the events an outcome produces, in the same transaction.
func (uc *SubmitTransaction) emit(ctx context.Context, repos Repositories, transaction domain.WagerTransaction, wallet domain.Wallet, entry domain.LedgerEntry, moved bool) error {
	events, err := eventsForOutcome(transaction, wallet, entry, moved)
	if err != nil {
		return err
	}
	return uc.events.record(ctx, repos, events...)
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

// resolve applies a reversal, parks it, or records its rejection.
func (uc *SubmitTransaction) resolve(
	ctx context.Context,
	repos Repositories,
	reversal domain.WagerTransaction,
	wallet domain.Wallet,
	at time.Time,
) (SubmitResult, error) {
	decision, err := resolveReference(ctx, repos, reversal)
	if err != nil {
		return SubmitResult{}, err
	}

	switch decision.outcome {
	case resolveWait:
		// Out-of-order delivery: the reversal overtook what it undoes. It is
		// recorded as waiting, with its own schedule and deadline, and the
		// worker takes it from here.
		parked, err := reversal.MarkPendingReference(at,
			uc.waiting.nextAttemptAt(at, 1), at.Add(uc.waiting.TTL))
		if err != nil {
			return SubmitResult{}, err
		}
		if err := repos.Transactions().Insert(ctx, parked); err != nil {
			return SubmitResult{}, err
		}
		if err := uc.emit(ctx, repos, parked, wallet, domain.LedgerEntry{}, false); err != nil {
			return SubmitResult{}, err
		}
		return SubmitResult{Transaction: parked, Balance: wallet.Balance()}, nil

	case resolveReject:
		return uc.recordReversalRejection(ctx, repos, reversal, decision.code, wallet, at)
	}

	applied, movement, err := applyReversal(ctx, repos, uc.ids, reversal, decision, wallet, at)
	if err != nil {
		var de *domain.Error
		if !errors.As(err, &de) {
			return SubmitResult{}, err
		}
		return uc.recordReversalRejection(ctx, repos, reversal, de.Code, wallet, at)
	}
	if err := uc.emit(ctx, repos, applied, movement.Wallet, movement.Entry, true); err != nil {
		return SubmitResult{}, err
	}

	return SubmitResult{Transaction: applied, Balance: movement.Wallet.Balance()}, nil
}

func (uc *SubmitTransaction) recordReversalRejection(
	ctx context.Context,
	repos Repositories,
	reversal domain.WagerTransaction,
	code domain.Code,
	wallet domain.Wallet,
	at time.Time,
) (SubmitResult, error) {
	rejected, err := reversal.Reject(code, at)
	if err != nil {
		return SubmitResult{}, err
	}
	if err := repos.Transactions().Insert(ctx, rejected); err != nil {
		return SubmitResult{}, err
	}
	if err := uc.emit(ctx, repos, rejected, wallet, domain.LedgerEntry{}, false); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{Transaction: rejected, Balance: wallet.Balance()}, nil
}

// applyReversal moves the money the reversal undoes and writes everything.
//
// It is shared with the worker: a reversal that waited and one that applied
// straight away must end in exactly the same state, and two copies of this
// would drift the first time one of them was fixed.
func applyReversal(
	ctx context.Context,
	repos Repositories,
	ids IDGenerator,
	reversal domain.WagerTransaction,
	decision resolution,
	wallet domain.Wallet,
	at time.Time,
) (domain.WagerTransaction, domain.Movement, error) {
	entryID, err := ids.NewLedgerEntryID(ctx)
	if err != nil {
		return domain.WagerTransaction{}, domain.Movement{}, err
	}

	var movement domain.Movement
	switch decision.direction {
	case domain.Credit:
		movement, err = wallet.Credit(entryID, reversal.ID(), reversal.Money(), at)
	default:
		movement, err = wallet.Debit(entryID, reversal.ID(), reversal.Money(), at)
		if err != nil && errors.Is(err, domain.ErrInsufficientFunds) {
			// Money that was already handed over and cannot be taken back. It
			// is a reconciliation problem, not a player hitting their limit,
			// and giving it the bet's code would bury it in that volume.
			err = &domain.Error{
				Code: domain.CodeReversalExceedsBalance,
				Detail: "reversing " + reversal.Money().String() +
					" needs more than the " + wallet.Balance().String() + " available",
			}
		}
	}
	if err != nil {
		return domain.WagerTransaction{}, domain.Movement{}, err
	}

	resolved, err := reversal.ResolveReference(decision.reference.ID(), at)
	if err != nil {
		return domain.WagerTransaction{}, domain.Movement{}, err
	}
	applied, err := resolved.MarkProcessed(movement.Wallet.Balance(), at)
	if err != nil {
		return domain.WagerTransaction{}, domain.Movement{}, err
	}

	if err := writeReversal(ctx, repos, applied, movement, wallet.Version()); err != nil {
		return domain.WagerTransaction{}, domain.Movement{}, err
	}
	return applied, movement, nil
}

// writeReversal persists an applied reversal. The transaction row may already
// exist -- when it waited -- so the caller says which.
func writeReversal(ctx context.Context, repos Repositories, applied domain.WagerTransaction, movement domain.Movement, expectedVersion int64) error {
	if applied.ReferenceAttempts() > 0 {
		if err := repos.Transactions().Update(ctx, applied); err != nil {
			return err
		}
	} else if err := repos.Transactions().Insert(ctx, applied); err != nil {
		return err
	}
	if err := repos.Wallets().UpdateBalance(ctx, movement.Wallet, expectedVersion); err != nil {
		return err
	}
	return repos.Ledger().Append(ctx, movement.Entry)
}
