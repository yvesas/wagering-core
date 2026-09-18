// Package sqs is the queue adapter. It is the only place that knows what SQS
// calls things; the app layer sees four methods and no transport vocabulary.
package sqs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/yvesas/wagering-core/internal/app"
)

// Config is what it takes to reach the broker and the queues.
type Config struct {
	// Endpoint is empty in production, where the SDK finds AWS by itself, and
	// points at the emulator locally.
	Endpoint string
	Region   string

	QueueName   string
	DLQName     string
	AccessKeyID string
	SecretKey   string

	VisibilityTimeout time.Duration
	WaitTime          time.Duration
	MaxReceiveCount   int
}

// Client wraps the SDK and the resolved queue url.
type Client struct {
	api      *sqs.Client
	queueURL string
	cfg      Config
}

// New builds the client and provisions the queues.
//
// Provisioning at start-up is deliberate and idempotent: CreateQueue with the
// same attributes is a no-op, so every replica can call it, for the same reason
// every replica runs the migrations.
func New(ctx context.Context, cfg Config) (*Client, error) {
	awsCfg, err := loadAWS(ctx, cfg)
	if err != nil {
		return nil, err
	}

	api := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})

	queueURL, err := provision(ctx, api, cfg)
	if err != nil {
		return nil, err
	}
	return &Client{api: api, queueURL: queueURL, cfg: cfg}, nil
}

func loadAWS(ctx context.Context, cfg Config) (aws.Config, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKeyID != "" {
		// The emulator ignores credentials but the SDK refuses to sign without
		// them, so these exist to be present rather than to be secret.
		options = append(options, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("loading aws configuration: %w", err)
	}
	return awsCfg, nil
}

// provision creates the dead-letter queue, then the main queue pointing at it.
//
// The order matters: the redrive policy needs the DLQ's ARN, so the DLQ has to
// exist first. Creating them the other way round produces a main queue with no
// redrive, and nothing ever tells you -- the messages simply keep coming back.
func provision(ctx context.Context, api *sqs.Client, cfg Config) (string, error) {
	dlqURL, err := createQueue(ctx, api, cfg.DLQName, map[string]string{
		string(types.QueueAttributeNameFifoQueue): "true",
	})
	if err != nil {
		return "", err
	}

	dlqARN, err := queueARN(ctx, api, dlqURL)
	if err != nil {
		return "", err
	}

	// maxReceiveCount and the redrive live on the queue, not in our code.
	// Counting attempts on our side would mean persisting the count, and then
	// it would exist in two places that disagree after the first restart.
	redrive := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"%d"}`,
		dlqARN, cfg.MaxReceiveCount)

	queueURL, err := createQueue(ctx, api, cfg.QueueName, map[string]string{
		string(types.QueueAttributeNameFifoQueue):                 "true",
		string(types.QueueAttributeNameContentBasedDeduplication): "false",
		string(types.QueueAttributeNameVisibilityTimeout):         strconv.Itoa(int(cfg.VisibilityTimeout.Seconds())),
		string(types.QueueAttributeNameRedrivePolicy):             redrive,
	})
	if err != nil {
		return "", err
	}
	return queueURL, nil
}

func createQueue(ctx context.Context, api *sqs.Client, name string, attributes map[string]string) (string, error) {
	out, err := api.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: attributes,
	})
	if err != nil {
		// An existing queue with different attributes is a real conflict, and
		// saying which queue makes it findable.
		return "", fmt.Errorf("provisioning queue %s: %w", name, err)
	}
	return aws.ToString(out.QueueUrl), nil
}

func queueARN(ctx context.Context, api *sqs.Client, url string) (string, error) {
	out, err := api.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return "", fmt.Errorf("reading the queue arn: %w", err)
	}
	arn := out.Attributes[string(types.QueueAttributeNameQueueArn)]
	if arn == "" {
		return "", errors.New("the queue reported no arn")
	}
	return arn, nil
}

// QueueURL exposes the resolved url, for a producer or a test.
func (c *Client) QueueURL() string { return c.queueURL }

// Receive long-polls for messages.
//
// Long polling is not an optimisation here: short polling would turn an idle
// queue into a request per loop iteration, which on a real account is a bill.
func (c *Client) Receive(ctx context.Context, max int) ([]app.QueueMessage, error) {
	out, err := c.api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.queueURL),
		MaxNumberOfMessages: int32(max),
		WaitTimeSeconds:     int32(c.cfg.WaitTime.Seconds()),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// Shutdown, not a failure.
			return nil, nil
		}
		return nil, fmt.Errorf("receiving: %w", err)
	}

	messages := make([]app.QueueMessage, 0, len(out.Messages))
	for _, m := range out.Messages {
		messages = append(messages, app.QueueMessage{
			ReceiptHandle: aws.ToString(m.ReceiptHandle),
			Body:          []byte(aws.ToString(m.Body)),
		})
	}
	return messages, nil
}

func (c *Client) Delete(ctx context.Context, receiptHandle string) error {
	_, err := c.api.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	})
	if err != nil {
		return fmt.Errorf("deleting a message: %w", err)
	}
	return nil
}

// Release makes the message visible again immediately.
//
// Setting the visibility to zero is the difference between "another instance
// picks this up now" and "nobody touches it until the timeout expires". A
// consumer that dies without doing this costs a full visibility timeout of
// delay on work it already knew it could not finish.
func (c *Client) Release(ctx context.Context, receiptHandle string) error {
	_, err := c.api.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.queueURL),
		ReceiptHandle:     aws.String(receiptHandle),
		VisibilityTimeout: 0,
	})
	if err != nil {
		return fmt.Errorf("releasing a message: %w", err)
	}
	return nil
}
