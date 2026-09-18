package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

type transactionRepository struct {
	q querier
}

const transactionColumns = `id, origin, kind, status, wallet_id, player_id, ` +
	`amount_minor, currency, provider_id, external_id, idempotency_key, payload_hash, ` +
	`round_id, game_id, reference_external_id, resolved_reference_id, ` +
	`reference_attempts, reference_next_attempt_at, reference_deadline_at, ` +
	`failure_code, balance_after_minor, created_at, updated_at`

func (r *transactionRepository) FindByID(ctx context.Context, id domain.TransactionID) (domain.WagerTransaction, error) {
	row := r.q.QueryRow(ctx,
		`SELECT `+transactionColumns+` FROM wager_transactions WHERE id = $1`,
		id.String())
	return scanTransaction(row)
}

// FindByBusinessID resolves the pair that identifies an operation to its
// provider. A reversal finds what it undoes through this, and a replay finds
// its stored result.
func (r *transactionRepository) FindByBusinessID(ctx context.Context, provider domain.ProviderID, external domain.ExternalTransactionID) (domain.WagerTransaction, error) {
	row := r.q.QueryRow(ctx, `
		SELECT `+transactionColumns+`
		  FROM wager_transactions
		 WHERE provider_id = $1 AND external_id = $2`,
		provider.String(), external.String())
	return scanTransaction(row)
}

// FindProcessedReversalOf returns the successful reversal of an operation.
//
// At most one row can match: a partial unique index on resolved_reference_id,
// restricted to PROCESSED, makes a second one impossible.
func (r *transactionRepository) FindProcessedReversalOf(ctx context.Context, reference domain.TransactionID) (domain.WagerTransaction, error) {
	row := r.q.QueryRow(ctx, `
		SELECT `+transactionColumns+`
		  FROM wager_transactions
		 WHERE resolved_reference_id = $1 AND status = 'PROCESSED'`,
		reference.String())
	return scanTransaction(row)
}

func (r *transactionRepository) Insert(ctx context.Context, tx domain.WagerTransaction) error {
	if !tx.IsInitialised() {
		return fmt.Errorf("inserting an uninitialised transaction")
	}

	_, err := r.q.Exec(ctx, `
		INSERT INTO wager_transactions (`+transactionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)`,
		tx.ID().String(),
		string(tx.Origin()),
		string(tx.Kind()),
		string(tx.Status()),
		tx.WalletID().String(),
		tx.PlayerID().String(),
		tx.Money().Minor(),
		tx.Money().Currency().String(),
		nullable(tx.ProviderID().String()),
		nullable(tx.ExternalID().String()),
		nullable(tx.IdempotencyKey().String()),
		nullable(tx.PayloadHash().String()),
		nullable(tx.RoundID().String()),
		nullable(tx.GameID().String()),
		nullable(tx.ReferenceExternalID().String()),
		nullable(tx.ResolvedReferenceID().String()),
		tx.ReferenceAttempts(),
		nullableTime(tx.ReferenceNextAttemptAt()),
		nullableTime(tx.ReferenceDeadlineAt()),
		nullable(string(tx.FailureCode())),
		nullableMinor(tx.BalanceAfter()),
		tx.CreatedAt(),
		tx.UpdatedAt(),
	)
	return translate(err)
}

// Update writes a transition.
//
// The WHERE clause refuses to move a row that is already terminal. The domain
// refuses the same transition, but the two guards answer different questions:
// the domain knows what the caller holds in memory, and this one knows what is
// actually stored. A worker that woke up late holds a stale PENDING and would
// happily overwrite a result that another instance already committed.
func (r *transactionRepository) Update(ctx context.Context, tx domain.WagerTransaction) error {
	if !tx.IsInitialised() {
		return fmt.Errorf("updating an uninitialised transaction")
	}

	tag, err := r.q.Exec(ctx, `
		UPDATE wager_transactions
		   SET status = $1,
		       resolved_reference_id = $2,
		       reference_attempts = $3,
		       reference_next_attempt_at = $4,
		       reference_deadline_at = $5,
		       failure_code = $6,
		       balance_after_minor = $7,
		       updated_at = $8
		 WHERE id = $9
		   AND status NOT IN ('PROCESSED', 'REJECTED', 'FAILED')`,
		string(tx.Status()),
		nullable(tx.ResolvedReferenceID().String()),
		tx.ReferenceAttempts(),
		nullableTime(tx.ReferenceNextAttemptAt()),
		nullableTime(tx.ReferenceDeadlineAt()),
		nullable(string(tx.FailureCode())),
		nullableMinor(tx.BalanceAfter()),
		tx.UpdatedAt(),
		tx.ID().String(),
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	var stored string
	err = r.q.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE id = $1`, tx.ID().String()).Scan(&stored)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return app.ErrNotFound
	case err != nil:
		return translate(err)
	default:
		return fmt.Errorf("%w: transaction %s is already %s",
			app.ErrConflict, tx.ID(), stored)
	}
}

// nullable turns an empty identifier into a NULL.
//
// The schema's shape constraints are written against NULL, not against the
// empty string: an internal opening must carry no provider id, and an empty
// string is very much a provider id as far as a CHECK is concerned.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableTime writes an instant only when it was set. A zero time.Time written
// as-is would land in the year 1, which a deadline comparison would then read
// as "expired long ago".
func nullableTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

// nullableMinor writes a Money only when it was actually set. An operation that
// has not been processed has no observed balance, and zero would be a lie.
func nullableMinor(m domain.Money) *int64 {
	if !m.IsInitialised() {
		return nil
	}
	minor := m.Minor()
	return &minor
}

func scanTransaction(row scanner) (domain.WagerTransaction, error) {
	var (
		rawID, rawOrigin, rawKind, rawStatus string
		rawWallet, rawPlayer, rawCurrency    string
		amountMinor                          int64
		createdAt, updatedAt                 time.Time

		rawProvider, rawExternal, rawKey, rawHash *string
		rawRound, rawGame, rawReference           *string
		rawResolved, rawFailure                   *string
		balanceAfterMinor                         *int64

		referenceAttempts                int
		nextAttemptAt, referenceDeadline *time.Time
	)

	if err := row.Scan(
		&rawID, &rawOrigin, &rawKind, &rawStatus, &rawWallet, &rawPlayer,
		&amountMinor, &rawCurrency, &rawProvider, &rawExternal, &rawKey, &rawHash,
		&rawRound, &rawGame, &rawReference, &rawResolved,
		&referenceAttempts, &nextAttemptAt, &referenceDeadline,
		&rawFailure, &balanceAfterMinor, &createdAt, &updatedAt,
	); err != nil {
		return domain.WagerTransaction{}, translate(err)
	}

	currency, err := domain.ParseCurrency(strings.TrimSpace(rawCurrency))
	if err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored currency: %w", err)
	}
	amount, err := domain.NewMoneyFromMinor(amountMinor, currency)
	if err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored amount: %w", err)
	}

	params := domain.RehydrateTransactionParams{
		Origin:                 domain.Origin(rawOrigin),
		Kind:                   domain.Kind(rawKind),
		Status:                 domain.Status(rawStatus),
		Money:                  amount,
		ReferenceAttempts:      referenceAttempts,
		ReferenceNextAttemptAt: derefTime(nextAttemptAt),
		ReferenceDeadlineAt:    derefTime(referenceDeadline),
		FailureCode:            domain.Code(deref(rawFailure)),
		CreatedAt:              createdAt,
		UpdatedAt:              updatedAt,
	}

	if params.ID, err = domain.ParseTransactionID(rawID); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored transaction id: %w", err)
	}
	if params.WalletID, err = domain.ParseWalletID(rawWallet); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored wallet id: %w", err)
	}
	if params.PlayerID, err = domain.ParsePlayerID(rawPlayer); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored player id: %w", err)
	}

	// The optional columns are parsed only when present. Parsing an absent one
	// would turn a legitimate NULL into an "empty id" rejection.
	if err := parseOptional(rawProvider, func(s string) error {
		params.ProviderID, err = domain.ParseProviderID(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored provider id: %w", err)
	}
	if err := parseOptional(rawExternal, func(s string) error {
		params.ExternalID, err = domain.ParseExternalTransactionID(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored external id: %w", err)
	}
	if err := parseOptional(rawKey, func(s string) error {
		params.IdempotencyKey, err = domain.ParseIdempotencyKey(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored idempotency key: %w", err)
	}
	if err := parseOptional(rawHash, func(s string) error {
		params.PayloadHash, err = domain.ParsePayloadHash(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored payload hash: %w", err)
	}
	if err := parseOptional(rawRound, func(s string) error {
		params.RoundID, err = domain.ParseRoundID(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored round id: %w", err)
	}
	if err := parseOptional(rawGame, func(s string) error {
		params.GameID, err = domain.ParseGameID(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored game id: %w", err)
	}
	if err := parseOptional(rawReference, func(s string) error {
		params.ReferenceExternalID, err = domain.ParseExternalTransactionID(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored reference id: %w", err)
	}
	if err := parseOptional(rawResolved, func(s string) error {
		params.ResolvedReferenceID, err = domain.ParseTransactionID(s)
		return err
	}); err != nil {
		return domain.WagerTransaction{}, fmt.Errorf("stored resolved reference id: %w", err)
	}

	if balanceAfterMinor != nil {
		if params.BalanceAfter, err = domain.NewMoneyFromMinor(*balanceAfterMinor, currency); err != nil {
			return domain.WagerTransaction{}, fmt.Errorf("stored balance after: %w", err)
		}
	}

	return domain.RehydrateTransaction(params)
}

func parseOptional(raw *string, parse func(string) error) error {
	if raw == nil || *raw == "" {
		return nil
	}
	return parse(*raw)
}

func derefTime(at *time.Time) time.Time {
	if at == nil {
		return time.Time{}
	}
	return *at
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ClaimDueReferences reserves pending reversals whose next attempt has come
// round, and holds them until the transaction ends.
//
// SKIP LOCKED is what lets several workers run: a row another instance is
// already holding is passed over rather than waited on, so two workers never
// take the same pending item and neither blocks behind the other. Without it,
// a second worker would queue up on the first one's row and the whole scan
// would serialise.
func (r *transactionRepository) ClaimDueReferences(ctx context.Context, now time.Time, limit int) ([]domain.WagerTransaction, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive, got %d", limit)
	}

	rows, err := r.q.Query(ctx, `
		SELECT `+transactionColumns+`
		  FROM wager_transactions
		 WHERE status = 'PENDING_REFERENCE'
		   AND reference_next_attempt_at <= $1
		 ORDER BY reference_next_attempt_at
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`,
		now, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var claimed []domain.WagerTransaction
	for rows.Next() {
		transaction, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, transaction)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return claimed, nil
}
