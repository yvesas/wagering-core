-- Waiting for a reference, and reversing an operation only once.
--
-- The wait lives in columns rather than in a worker's memory, for the same
-- reason the idempotency record is the transaction row itself: a process that
-- restarts must find the pending work where it left it, and two sources about
-- the same fact drift apart.

-- +goose Up

ALTER TABLE wager_transactions
    -- How many times the reference has been looked for. Bounded, so a wait
    -- cannot become a request that never answers and never gives up.
    ADD COLUMN reference_attempts INTEGER NOT NULL DEFAULT 0,

    -- When the worker may look again. Exponential and jittered, written by the
    -- application; the column is only where it is remembered across restarts.
    ADD COLUMN reference_next_attempt_at TIMESTAMPTZ,

    -- The deadline. Attempts and a TTL together, because either alone is weak:
    -- with exponential backoff, five attempts might be thirty seconds or thirty
    -- minutes, and a long window with a short backoff is thousands of useless
    -- lookups.
    ADD COLUMN reference_deadline_at TIMESTAMPTZ;

ALTER TABLE wager_transactions
    -- Only a reversal waits, and only while it is waiting.
    ADD CONSTRAINT wager_transactions_reference_wait CHECK (
        (status = 'PENDING_REFERENCE'
            AND kind IN ('REFUND', 'ROLLBACK')
            AND reference_next_attempt_at IS NOT NULL
            AND reference_deadline_at IS NOT NULL)
        OR status <> 'PENDING_REFERENCE'
    ),
    ADD CONSTRAINT wager_transactions_reference_attempts CHECK (reference_attempts >= 0);

-- At most one successful reversal per operation.
--
-- The minimum the requirements ask for is "not two of the same type", which
-- leaves the door open: a REFUND and then a ROLLBACK of the same bet are
-- different types and return the same 25.00 twice. This index is the stronger
-- rule -- one successful reversal, of any kind -- and it is what actually keeps
-- the money coherent.
--
-- It is partial on PROCESSED because a rejected reversal consumed nothing: the
-- operation is still reversible by someone else.
--
-- Reversing a REFUND is still allowed, and is not the same thing as reversing
-- its BET a second time: the rollback points at the refund's own id.
CREATE UNIQUE INDEX wager_transactions_one_reversal_per_operation
    ON wager_transactions (resolved_reference_id)
    WHERE resolved_reference_id IS NOT NULL AND status = 'PROCESSED';

-- The worker's scan: pendings whose next attempt has come round, oldest first.
CREATE INDEX wager_transactions_due_references
    ON wager_transactions (reference_next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

-- +goose Down

DROP INDEX IF EXISTS wager_transactions_due_references;
DROP INDEX IF EXISTS wager_transactions_one_reversal_per_operation;

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_transactions_reference_attempts,
    DROP CONSTRAINT IF EXISTS wager_transactions_reference_wait;

ALTER TABLE wager_transactions
    DROP COLUMN IF EXISTS reference_deadline_at,
    DROP COLUMN IF EXISTS reference_next_attempt_at,
    DROP COLUMN IF EXISTS reference_attempts;
