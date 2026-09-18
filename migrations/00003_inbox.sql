-- The inbox: which messages this consumer has already handled.
--
-- It deduplicates the *message*; idempotency (migration 1) deduplicates the
-- financial *operation*. They are not the same thing. Two different messages
-- carrying one operation pass the inbox and are stopped by idempotency; the
-- same message delivered twice is stopped here, without touching the domain at
-- all.
--
-- The row is written in the same transaction as the domain changes it causes.
-- That is the whole point: a completed handling and its record cannot disagree
-- because there is no moment at which only one of them exists.

-- +goose Up

CREATE TABLE inbox_messages (
    -- Two consumers must be able to handle the same message independently, so
    -- the identity is the pair. One of them cannot swallow the other's work.
    consumer_name TEXT NOT NULL,

    -- The envelope's own messageId, chosen by the producer -- not the queue's.
    -- A broker's message id can change on redrive, which would make a
    -- redelivery look like a new message.
    message_id TEXT NOT NULL,

    -- A redelivery carrying different content is a producer reusing an
    -- identifier. Storing the hash makes that visible instead of silently
    -- accepted.
    payload_hash TEXT NOT NULL,

    received_at  TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,

    CONSTRAINT inbox_messages_identity PRIMARY KEY (consumer_name, message_id)
);

-- +goose Down

DROP TABLE IF EXISTS inbox_messages;
