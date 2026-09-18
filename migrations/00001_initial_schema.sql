-- The schema enforces the financial invariants a second time.
--
-- The domain already knows them, and checks them in memory where they are cheap
-- to exercise. These constraints exist because the domain is code and code has
-- bugs: this is the layer that survives a bad deploy, a misbehaving instance and
-- a hand-written UPDATE at 3am. Every one of them has a test that violates it on
-- purpose.
--
-- Money is a BIGINT of minor units plus its own currency column. Never FLOAT,
-- and never NUMERIC: the conversion is exact in both directions and no driver
-- gets a chance to hand back a float64 on the way.
--
-- Identifiers are TEXT, not UUID. Internal ones are UUIDv7 minted by an adapter,
-- but external ones are whatever the provider sent -- "transaction-123" is
-- legitimate input, and a UUID column would reject it.

-- +goose Up

CREATE TABLE wallets (
    id            TEXT        PRIMARY KEY,
    player_id     TEXT        NOT NULL,
    currency      CHAR(3)     NOT NULL,
    balance_minor BIGINT      NOT NULL,
    version       BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_balance_non_negative CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_positive     CHECK (version >= 1),
    CONSTRAINT wallets_currency_iso4217     CHECK (currency ~ '^[A-Z]{3}$'),

    -- One wallet per player and currency. This is what makes a second opening
    -- a conflict instead of a duplicate balance.
    CONSTRAINT wallets_one_per_player_and_currency UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
    id           TEXT        PRIMARY KEY,
    origin       TEXT        NOT NULL,
    kind         TEXT        NOT NULL,
    status       TEXT        NOT NULL,
    wallet_id    TEXT        NOT NULL REFERENCES wallets (id),
    player_id    TEXT        NOT NULL,
    amount_minor BIGINT      NOT NULL,
    currency     CHAR(3)     NOT NULL,

    -- Provider metadata. All NULL on an internal opening, all present on
    -- anything that came from outside; the shape checks below enforce it.
    provider_id     TEXT,
    external_id     TEXT,
    idempotency_key TEXT,
    payload_hash    TEXT,
    round_id        TEXT,
    game_id         TEXT,

    reference_external_id TEXT,
    resolved_reference_id TEXT REFERENCES wager_transactions (id),

    failure_code        TEXT,
    balance_after_minor BIGINT,

    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT wager_transactions_origin CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    CONSTRAINT wager_transactions_kind CHECK (
        kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')
    ),
    CONSTRAINT wager_transactions_status CHECK (
        status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')
    ),
    CONSTRAINT wager_transactions_currency CHECK (currency ~ '^[A-Z]{3}$'),

    -- LOSS records how a round ended and moves nothing. Everything else moves
    -- money, so zero would be a movement of nothing.
    CONSTRAINT wager_transactions_amount_policy CHECK (
        (kind = 'LOSS' AND amount_minor = 0)
        OR (kind <> 'LOSS' AND amount_minor > 0)
    ),

    -- An internal row carrying a provider id would mean the system raised an
    -- operation on someone's behalf. It stops here.
    CONSTRAINT wager_transactions_internal_shape CHECK (
        origin <> 'INTERNAL' OR (
            kind = 'OPENING'
            AND provider_id IS NULL
            AND external_id IS NULL
            AND idempotency_key IS NULL
            AND payload_hash IS NULL
            AND round_id IS NULL
            AND game_id IS NULL
            AND reference_external_id IS NULL
        )
    ),

    -- OPENING from outside would let a provider mint balance.
    CONSTRAINT wager_transactions_external_shape CHECK (
        origin <> 'EXTERNAL' OR (
            kind <> 'OPENING'
            AND provider_id IS NOT NULL
            AND external_id IS NOT NULL
            AND idempotency_key IS NOT NULL
            AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL
            AND game_id IS NOT NULL
        )
    ),

    CONSTRAINT wager_transactions_reference_policy CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_id IS NOT NULL)
        OR (kind NOT IN ('REFUND', 'ROLLBACK') AND reference_external_id IS NULL)
    ),

    -- A rejection a provider cannot branch on is not a rejection.
    CONSTRAINT wager_transactions_failure_code CHECK (
        (status IN ('REJECTED', 'FAILED') AND failure_code IS NOT NULL)
        OR (status NOT IN ('REJECTED', 'FAILED') AND failure_code IS NULL)
    ),

    CONSTRAINT wager_transactions_balance_after CHECK (
        balance_after_minor IS NULL OR balance_after_minor >= 0
    )
);

-- The business identity of an operation. A reversal resolves against this pair,
-- and it is what stops the same operation being reapplied under another key.
CREATE UNIQUE INDEX wager_transactions_business_identity
    ON wager_transactions (provider_id, external_id)
    WHERE origin = 'EXTERNAL';

-- The idempotency key is transport level and scoped to its provider. Two
-- providers may legitimately choose the same string.
CREATE UNIQUE INDEX wager_transactions_idempotency_key
    ON wager_transactions (provider_id, idempotency_key)
    WHERE origin = 'EXTERNAL';

-- A wallet is credited once at opening. A second OPENING row would be a second
-- initial balance out of nowhere.
CREATE UNIQUE INDEX wager_transactions_one_opening_per_wallet
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE INDEX wager_transactions_by_wallet ON wager_transactions (wallet_id);

CREATE INDEX wager_transactions_pending_reference
    ON wager_transactions (status)
    WHERE status = 'PENDING_REFERENCE';

CREATE TABLE wallet_ledger_entries (
    -- seq gives the ledger a stable total order, which cursor pagination needs:
    -- created_at can tie, and a tie makes a cursor skip or repeat rows.
    seq BIGSERIAL NOT NULL UNIQUE,

    id                   TEXT        PRIMARY KEY,
    wallet_id            TEXT        NOT NULL REFERENCES wallets (id),
    transaction_id       TEXT        NOT NULL REFERENCES wager_transactions (id),
    direction            TEXT        NOT NULL,
    amount_minor         BIGINT      NOT NULL,
    balance_before_minor BIGINT      NOT NULL,
    balance_after_minor  BIGINT      NOT NULL,
    currency             CHAR(3)     NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT ledger_direction CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT ledger_currency CHECK (currency ~ '^[A-Z]{3}$'),

    -- The direction carries the sign, so the amount is always positive.
    CONSTRAINT ledger_amount_positive CHECK (amount_minor > 0),

    CONSTRAINT ledger_balances_non_negative CHECK (
        balance_before_minor >= 0 AND balance_after_minor >= 0
    ),

    -- The arithmetic of the entry, checked by the database. The domain checks it
    -- too; this is the copy that survives a bug in the domain.
    CONSTRAINT ledger_arithmetic CHECK (
        (direction = 'DEBIT' AND balance_after_minor = balance_before_minor - amount_minor)
        OR (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor)
    ),

    -- One entry per transaction per wallet: this is the constraint that turns a
    -- duplicate delivery into a rejected insert instead of a second debit.
    CONSTRAINT ledger_one_per_transaction_and_wallet UNIQUE (wallet_id, transaction_id)
);

CREATE INDEX wallet_ledger_entries_by_wallet ON wallet_ledger_entries (wallet_id, seq);

-- Append-only, enforced by the database.
--
-- A REVOKE alone would not do it: it does not bind the table owner, and
-- migrations run as the owner. The trigger binds everyone.
-- +goose StatementBegin
CREATE FUNCTION wallet_ledger_entries_reject_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % is not allowed', tg_op
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER wallet_ledger_entries_no_row_mutation
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_entries_reject_mutation();

-- TRUNCATE does not fire row-level triggers, so without this one the whole
-- guard above has a door next to it.
CREATE TRIGGER wallet_ledger_entries_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_ledger_entries_reject_mutation();

-- +goose Down

DROP TRIGGER IF EXISTS wallet_ledger_entries_no_truncate ON wallet_ledger_entries;
DROP TRIGGER IF EXISTS wallet_ledger_entries_no_row_mutation ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS wallet_ledger_entries_reject_mutation();
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
