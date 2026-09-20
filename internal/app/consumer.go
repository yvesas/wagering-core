package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// QueueMessage is one delivery, already lifted out of whatever the broker calls
// things. The app layer never sees an SQS type.
type QueueMessage struct {
	// ReceiptHandle is how the broker identifies this delivery, for deleting it
	// or handing it back early.
	ReceiptHandle string

	// Body is the raw envelope, still unparsed: the payload hash is taken from
	// the decoded business fields, not from these bytes, so that a producer
	// reformatting its JSON does not look like a different operation.
	Body []byte
}

// Queue is what the consumer needs from a broker.
//
// It is four methods and no transport vocabulary, so the same consumer would
// run against a different broker by writing a different adapter -- and, more
// usefully here, it can be driven by a fake in a test.
type Queue interface {
	Receive(ctx context.Context, max int) ([]QueueMessage, error)

	// Delete removes a message. It is only ever called after the handling has
	// been committed.
	Delete(ctx context.Context, receiptHandle string) error

	// Release hands a message back for redelivery without waiting out its
	// visibility timeout. A consumer that dies without calling this leaves the
	// message invisible for the full timeout before anyone retries.
	Release(ctx context.Context, receiptHandle string) error
}

// Envelope is the message shape the producer sends.
type Envelope struct {
	MessageID  string       `json:"messageId"`
	Type       string       `json:"type"`
	OccurredAt time.Time    `json:"occurredAt"`
	Data       EnvelopeData `json:"data"`
}

// EnvelopeData is the operation itself.
type EnvelopeData struct {
	ProviderID          string    `json:"providerId"`
	ExternalID          string    `json:"externalTransactionId"`
	IdempotencyKey      string    `json:"idempotencyKey"`
	PlayerID            string    `json:"playerId"`
	WalletID            string    `json:"walletId"`
	RoundID             string    `json:"roundId"`
	GameID              string    `json:"gameId"`
	Kind                string    `json:"kind"`
	Money               MoneyData `json:"money"`
	ReferenceExternalID string    `json:"referenceExternalTransactionId,omitempty"`
}

// MoneyData is money on the wire: a decimal string, never a number.
type MoneyData struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// TypeWagerTransactionRequested is the only envelope type this consumer
// handles. Anything else is a producer mistake, not work for us.
const TypeWagerTransactionRequested = "WagerTransactionRequested"

// Command turns the envelope into what the use case takes.
//
// On the queue the idempotency key is a field of the envelope, where HTTP puts
// it in a header. That is the only difference between the two paths, and it
// ends here: both build the same command and go through the same code.
func (e Envelope) Command() SubmitCommand {
	return SubmitCommand{
		IdempotencyKey:      e.Data.IdempotencyKey,
		ProviderID:          e.Data.ProviderID,
		ExternalID:          e.Data.ExternalID,
		PlayerID:            e.Data.PlayerID,
		WalletID:            e.Data.WalletID,
		RoundID:             e.Data.RoundID,
		GameID:              e.Data.GameID,
		Kind:                e.Data.Kind,
		Amount:              e.Data.Money.Amount,
		Currency:            e.Data.Money.Currency,
		ReferenceExternalID: e.Data.ReferenceExternalID,
	}
}

// ConsumerConfig tunes one consumer loop.
type ConsumerConfig struct {
	// Name identifies this consumer in the inbox. Two consumers must be able to
	// handle the same message independently, so it is part of the key.
	Name string

	// BatchSize is how many messages one receive asks for.
	BatchSize int

	// IdleBackoff is how long to wait after an error before polling again, so a
	// broken broker is not hammered.
	IdleBackoff time.Duration
}

// Consumer applies operations delivered by a queue.
type Consumer struct {
	queue  Queue
	uow    UnitOfWork
	submit *SubmitTransaction
	clock  Clock
	cfg    ConsumerConfig
	logger *slog.Logger
}

func NewConsumer(queue Queue, uow UnitOfWork, submit *SubmitTransaction, clock Clock, cfg ConsumerConfig, logger *slog.Logger) *Consumer {
	if cfg.Name == "" {
		cfg.Name = "wager-transactions"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10
	}
	if cfg.IdleBackoff <= 0 {
		cfg.IdleBackoff = time.Second
	}
	return &Consumer{queue: queue, uow: uow, submit: submit, clock: clock, cfg: cfg, logger: logger}
}

// Run polls until the context is done.
//
// On shutdown it stops asking for work immediately -- the cancelled context
// makes the next Receive return at once -- and whatever is already in hand is
// finished by the current RunOnce before this returns.
func (c *Consumer) Run(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}

		handled, err := c.RunOnce(ctx)
		switch {
		case err != nil && !errors.Is(err, context.Canceled):
			c.logger.Error("consuming",
				slog.String("consumer", c.cfg.Name),
				slog.String("error", err.Error()))
			if !sleep(ctx, c.cfg.IdleBackoff) {
				return
			}
		case handled == 0:
			// Long polling already blocked in Receive, so an empty batch means
			// the queue is quiet rather than that we are spinning.
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// RunOnce receives a batch and handles every message in it.
//
// Messages already in hand are finished even when the context is cancelled --
// that is what "stop fetching, then finish what is in flight" means. The
// deadline that bounds it is the caller's shutdown timeout, and anything that
// does not fit has its visibility released so another instance gets it without
// waiting out the timeout.
func (c *Consumer) RunOnce(ctx context.Context) (int, error) {
	messages, err := c.queue.Receive(ctx, c.cfg.BatchSize)
	if err != nil {
		return 0, err
	}

	for _, message := range messages {
		c.handle(ctx, message)
	}
	return len(messages), nil
}

// outcome is what to do with the message once the handling is decided.
type outcome int

const (
	outcomeDelete outcome = iota
	outcomeRelease
)

func (c *Consumer) handle(ctx context.Context, message QueueMessage) {
	envelope, cmd, err := decodeEnvelope(message.Body)
	if err != nil {
		// A malformed envelope cannot be recorded against a message id we could
		// not read, and redelivering it would repeat the same failure until the
		// dead-letter queue takes it. Dropping it with a loud log is the honest
		// choice; the log line is the audit trail.
		c.logger.Error("discarding a malformed envelope",
			slog.String("consumer", c.cfg.Name),
			slog.String("error", err.Error()))
		c.finish(ctx, message, outcomeDelete)
		return
	}

	// The use case refuses a call with no identity, and rightly so: on this port
	// the caller is the envelope, and an envelope that cannot name a provider
	// names nobody. It is dropped with the malformed ones rather than released,
	// because redelivering it would fail in exactly the same way until the
	// dead-letter queue took it.
	identity, err := c.identity(envelope)
	if err != nil {
		c.logger.Error("discarding a message that names no provider",
			slog.String("consumer", c.cfg.Name),
			slog.String("messageId", envelope.MessageID),
			slog.String("error", err.Error()))
		c.finish(ctx, message, outcomeDelete)
		return
	}

	result, decided, err := c.apply(ctx, envelope, cmd, identity)
	if err != nil {
		// Transient: hand it straight back rather than leaving it invisible for
		// the whole visibility timeout.
		c.logger.Warn("releasing a message for redelivery",
			slog.String("consumer", c.cfg.Name),
			slog.String("messageId", envelope.MessageID),
			slog.String("error", err.Error()))
		c.finish(ctx, message, outcomeRelease)
		return
	}

	if decided {
		c.logger.Info("applied from the queue",
			slog.String("consumer", c.cfg.Name),
			slog.String("messageId", envelope.MessageID),
			slog.String("transactionId", result.Transaction.ID().String()),
			slog.String("status", string(result.Transaction.Status())))
	}
	c.finish(ctx, message, outcomeDelete)
}

// apply runs the handling and its inbox record in one transaction.
//
// The second return says whether this delivery did the work, as opposed to
// finding it already done.
func (c *Consumer) apply(ctx context.Context, envelope Envelope, cmd SubmitCommand, identity Identity) (SubmitResult, bool, error) {
	hash, err := cmd.PayloadHash()
	if err != nil {
		return SubmitResult{}, false, err
	}

	var (
		result SubmitResult
		worked bool
	)

	// The shutdown path cancels the context, and the work already in hand still
	// has to finish. WithoutCancel gives it a context that will not be pulled
	// out from under a commit; the caller's shutdown deadline is what bounds it.
	// The message id is what caused this work, so events recorded here can be
	// traced back to the delivery that produced them.
	work := WithIdentity(
		WithCorrelationID(context.WithoutCancel(ctx), envelope.MessageID), identity)

	err = c.uow.Do(work, func(ctx context.Context, repos Repositories) error {
		now := c.clock.Now()

		claimErr := repos.Inbox().Claim(ctx, InboxMessage{
			ConsumerName: c.cfg.Name,
			MessageID:    envelope.MessageID,
			PayloadHash:  hash,
			ReceivedAt:   now,
		})
		switch {
		case errors.Is(claimErr, ErrConflict):
			// Seen before. This is the duplicate delivery the inbox exists to
			// absorb: it costs one refused insert and touches nothing else.
			return c.alreadySeen(ctx, repos, envelope, hash)
		case claimErr != nil:
			return claimErr
		}

		applied, err := c.submit.ExecuteIn(ctx, repos, cmd)
		if err != nil {
			var de *domain.Error
			if !errors.As(err, &de) {
				// Infrastructure or a conflict: roll back and let the message
				// come round again.
				return err
			}
			// The command itself is unusable -- a currency that does not exist,
			// an amount the domain refuses. Redelivering repeats the same
			// failure forever, so the message is recorded as handled and the
			// rejection is the record.
			c.logger.Warn("the queue delivered an unusable command",
				slog.String("consumer", c.cfg.Name),
				slog.String("messageId", envelope.MessageID),
				slog.String("code", string(de.Code)))
		} else {
			result = applied
		}

		worked = true
		return repos.Inbox().Complete(ctx, c.cfg.Name, envelope.MessageID, now)
	})
	if err != nil {
		return SubmitResult{}, false, err
	}
	return result, worked, nil
}

// identity is who a delivery acts as.
//
// It comes from the envelope, and the trust anchor is the broker: the queue's
// access policy decides who may put a message on it, and a producer that is
// allowed to write can name any provider in the body. That is REQ-SEC-005 read
// honestly -- the credential check happened at the broker, not here -- and it
// is worth stating plainly rather than implying a token was verified.
//
// What does *not* change is everything after this point. The same use case runs
// with the same rules: the provider named here is the one the operation is
// recorded under, it cannot reach another provider's operations, and the domain
// still refuses whatever it would refuse over HTTP.
func (c *Consumer) identity(envelope Envelope) (Identity, error) {
	return NewIdentity(IdentityParams{
		// The consumer, not the provider: the subject says which credential
		// this came through, and here that is the queue itself.
		Subject:    "queue:" + c.cfg.Name,
		ProviderID: envelope.Data.ProviderID,

		// Submitting only. A delivery never reads back someone's operations and
		// never opens a wallet, so granting either would widen the queue's
		// reach past what any message can ask for.
		Scopes: []string{string(ScopeSubmit)},
	})
}

// alreadySeen decides what a repeat delivery means.
func (c *Consumer) alreadySeen(ctx context.Context, repos Repositories, envelope Envelope, hash domain.PayloadHash) error {
	seen, err := repos.Inbox().Find(ctx, c.cfg.Name, envelope.MessageID)
	if err != nil {
		return err
	}

	if seen.PayloadHash != hash {
		// The same message id carrying different content is a producer reusing
		// an identifier. Applying it would be applying an operation under
		// someone else's identity; the message is dropped and the log is loud.
		c.logger.Error("a repeated message id carries different content",
			slog.String("consumer", c.cfg.Name),
			slog.String("messageId", envelope.MessageID))
		return nil
	}

	if !seen.Handled() {
		// Received before but never completed: the process died between the
		// claim and the commit, so that attempt rolled back entirely. Rolling
		// back again lets it be redelivered and genuinely retried.
		return fmt.Errorf("%w: message %s was claimed but never completed",
			ErrSerializationFailure, envelope.MessageID)
	}
	return nil
}

// finish deletes or releases the message.
//
// Deleting happens only after the commit. Deleting first would lose the
// operation if the commit failed; deleting after can deliver twice, and that is
// exactly what the inbox absorbs. The asymmetry is the whole reason it exists.
func (c *Consumer) finish(ctx context.Context, message QueueMessage, what outcome) {
	// The broker call has to survive a cancelled context, or a shutdown would
	// leave every in-flight message to time out.
	ctx = context.WithoutCancel(ctx)

	var err error
	switch what {
	case outcomeRelease:
		err = c.queue.Release(ctx, message.ReceiptHandle)
	default:
		err = c.queue.Delete(ctx, message.ReceiptHandle)
	}
	if err != nil {
		// The work is committed either way. A failed delete means one
		// redelivery, which the inbox will absorb.
		c.logger.Warn("finishing a message",
			slog.String("consumer", c.cfg.Name),
			slog.String("error", err.Error()))
	}
}

// decodeEnvelope reads the message body.
//
// Unknown fields are refused here for the same reason they are on the HTTP
// edge: a producer sending "ammount" has a bug, and quietly dropping the field
// would turn it into a wrong balance nobody can explain.
func decodeEnvelope(body []byte) (Envelope, SubmitCommand, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, SubmitCommand{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if envelope.MessageID == "" {
		// Without an id there is nothing to deduplicate against, so this
		// message can never be handled safely.
		return Envelope{}, SubmitCommand{}, fmt.Errorf("%w: the envelope has no messageId", ErrInvalidInput)
	}
	if envelope.Type != TypeWagerTransactionRequested {
		return Envelope{}, SubmitCommand{}, fmt.Errorf("%w: unknown envelope type %q",
			ErrInvalidInput, envelope.Type)
	}
	if envelope.Data.IdempotencyKey == "" {
		// The queue carries the key in the body where HTTP carries it in a
		// header. Computing one here would be inventing idempotency the
		// producer never asked for.
		return Envelope{}, SubmitCommand{}, fmt.Errorf("%w: the envelope has no idempotencyKey", ErrInvalidInput)
	}
	return envelope, envelope.Command(), nil
}
