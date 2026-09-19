-- The outbox: events written in the same transaction as the fact they describe.
--
-- A database and a broker do not share a transaction. Publishing before the
-- commit announces something that may not have happened; publishing after it,
-- outside the transaction, loses the event if the process dies in between.
-- Writing the event here and publishing later is the only shape that has
-- neither failure.

-- +goose Up

CREATE TABLE outbox_events (
    -- seq gives the publisher a stable order. Events of one aggregate go out in
    -- the order the facts happened, because that is the order they were
    -- written.
    seq BIGSERIAL NOT NULL UNIQUE,

    -- Minted when the event is written, never when it is published.
    -- Republishing keeps it, which is what lets a consumer deduplicate -- and
    -- at-least-once delivery means it will have to.
    event_id TEXT PRIMARY KEY,

    event_type   TEXT        NOT NULL,
    event_version INTEGER    NOT NULL,
    aggregate_id TEXT        NOT NULL,

    -- The values at the instant the fact happened, serialised then. Not a
    -- reference to be resolved later: a payload rendered at publish time would
    -- report the state of *now*, and the same event would say different things
    -- depending on when the worker woke up.
    payload JSONB NOT NULL,

    correlation_id TEXT,
    causation_id   TEXT,

    occurred_at TIMESTAMPTZ NOT NULL,

    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    published_at    TIMESTAMPTZ,
    last_error      TEXT,

    CONSTRAINT outbox_events_version_positive CHECK (event_version >= 1),
    CONSTRAINT outbox_events_attempts_non_negative CHECK (attempts >= 0)
);

-- The publisher's scan: unpublished events whose next attempt has come round,
-- oldest first. Partial, so the index does not grow with everything ever sent.
CREATE INDEX outbox_events_pending
    ON outbox_events (next_attempt_at, seq)
    WHERE published_at IS NULL;

-- Reading an aggregate's history, and the reconciliation checks that will need
-- it in phase 10.
CREATE INDEX outbox_events_by_aggregate ON outbox_events (aggregate_id, seq);

-- +goose Down

DROP TABLE IF EXISTS outbox_events;
