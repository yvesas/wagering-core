package sqs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/yvesas/wagering-core/internal/app"
)

// eventEnvelope is the contract a consumer of our events reads.
//
// It carries the event's identity, what it is, what it is about, and when it
// happened -- and the payload as a typed object, not a string. A consumer
// routes on eventType and version, so both are top level rather than buried.
type eventEnvelope struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	Version       int             `json:"version"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId,omitempty"`
	CausationID   string          `json:"causationId,omitempty"`
	OccurredAt    string          `json:"occurredAt"`
	Data          json.RawMessage `json:"data"`
}

// EventPublisher sends outbox records to the events queue.
type EventPublisher struct {
	api      *sqs.Client
	queueURL string
}

// NewEventPublisher provisions the destination and returns the publisher.
//
// The destination is provisioned here for the same reason the inbound queues
// are: idempotently, at start-up, so no deploy step has to remember it.
func NewEventPublisher(ctx context.Context, cfg Config, eventsQueueName string) (*EventPublisher, error) {
	awsCfg, err := loadAWS(ctx, cfg)
	if err != nil {
		return nil, err
	}

	api := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})

	url, err := createQueue(ctx, api, eventsQueueName, map[string]string{
		"FifoQueue": "true",
	})
	if err != nil {
		return nil, err
	}
	return &EventPublisher{api: api, queueURL: url}, nil
}

// QueueURL exposes the destination, for a test that wants to read it back.
func (p *EventPublisher) QueueURL() string { return p.queueURL }

// Publish sends one event.
//
// The group is the aggregate, so a listener following one wallet sees its
// changes in order while unrelated aggregates stay parallel -- the same
// granularity the rest of the system coordinates at.
//
// The deduplication id is the event id, which is stable across a republish.
// That is a convenience, not the guarantee: the broker's window is five minutes
// and a republish can happen later, so the consumer still deduplicates on the
// event id itself.
func (p *EventPublisher) Publish(ctx context.Context, record app.OutboxRecord) error {
	body, err := json.Marshal(eventEnvelope{
		EventID:       record.EventID,
		EventType:     string(record.Event.Type),
		Version:       record.Event.Version,
		AggregateID:   record.Event.AggregateID,
		CorrelationID: record.CorrelationID,
		CausationID:   record.CausationID,
		OccurredAt:    record.Event.OccurredAt.UTC().Format(time.RFC3339Nano),
		Data:          record.Event.Payload,
	})
	if err != nil {
		return fmt.Errorf("serialising event %s: %w", record.EventID, err)
	}

	_, err = p.api.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String(record.Event.AggregateID),
		MessageDeduplicationId: aws.String(record.EventID),
	})
	if err != nil {
		return fmt.Errorf("publishing event %s: %w", record.EventID, err)
	}
	return nil
}
