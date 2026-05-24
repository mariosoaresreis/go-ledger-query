// Package kafka implements the Kafka consumer adapter for the query service.
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

const (
	Topic      = "ledger.transactions"
	GroupID    = "ledger-query-projection"
	DLQSuffix  = ".DLQ"
	MaxRetries = 5
)

// Consumer subscribes to the ledger.transactions topic and forwards every
// decoded Envelope to the Dispatcher. Offsets are committed only after the
// handler returns nil — at-least-once delivery guarantee.
// After MaxRetries consecutive failures on the same message, the raw bytes
// are forwarded to a dead-letter topic (Topic + DLQSuffix) and the offset
// is committed so the partition can make progress.
type Consumer struct {
	reader     *kafkago.Reader
	dlqWriter  *kafkago.Writer // nil when DLQ broker is not configured
	dispatcher ports.EventDispatcher
	log        *zap.Logger
	// retryCounts tracks consecutive failure count per (partition, offset).
	// Cleared on commit so memory stays bounded.
	retryCounts map[string]int
}

func NewConsumer(brokers []string, dispatcher ports.EventDispatcher, log *zap.Logger) *Consumer {
	r := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:        brokers,
		GroupID:        GroupID,
		Topic:          Topic,
		MinBytes:       1,
		MaxBytes:       10e6,
		CommitInterval: time.Second,
		StartOffset:    kafkago.FirstOffset,
		MaxWait:        5 * time.Second,
	})

	// DLQ writer uses the same brokers as the main consumer.
	dlq := &kafkago.Writer{
		Addr:         kafkago.TCP(brokers...),
		Topic:        Topic + DLQSuffix,
		Balancer:     &kafkago.LeastBytes{},
		BatchTimeout: 100 * time.Millisecond,
	}

	return &Consumer{
		reader:      r,
		dlqWriter:   dlq,
		dispatcher:  dispatcher,
		log:         log,
		retryCounts: make(map[string]int),
	}
}

// Start runs the consume loop until ctx is cancelled.
func (c *Consumer) Start(ctx context.Context) error {
	c.log.Info("kafka consumer starting",
		zap.String("topic", Topic),
		zap.String("group", GroupID),
		zap.Int("max_retries", MaxRetries),
	)

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				c.log.Info("kafka consumer stopping (context cancelled)")
				return nil
			}
			c.log.Error("kafka fetch error", zap.Error(err))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		msgKey := msgKey(msg)

		if err := c.handle(ctx, msg); err != nil {
			c.retryCounts[msgKey]++
			attempts := c.retryCounts[msgKey]

			if attempts >= MaxRetries {
				c.log.Error("message exceeded max retries — routing to DLQ",
					zap.Error(err),
					zap.Int64("offset", msg.Offset),
					zap.Int("partition", msg.Partition),
					zap.Int("attempts", attempts),
				)
				c.sendToDLQ(ctx, msg, err)
				// Commit the offset so the partition makes progress.
				if commitErr := c.reader.CommitMessages(ctx, msg); commitErr != nil {
					c.log.Error("offset commit after DLQ failed", zap.Error(commitErr))
				}
				delete(c.retryCounts, msgKey)
			} else {
				c.log.Error("event handling failed — will retry",
					zap.Error(err),
					zap.Int64("offset", msg.Offset),
					zap.Int("partition", msg.Partition),
					zap.Int("attempt", attempts),
					zap.Int("max_retries", MaxRetries),
				)
				time.Sleep(time.Duration(attempts*200) * time.Millisecond) // linear back-off
			}
			continue
		}

		// Success — commit and clear retry counter.
		delete(c.retryCounts, msgKey)
		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			c.log.Error("offset commit failed", zap.Error(err))
		}
	}
}

func (c *Consumer) handle(ctx context.Context, msg kafkago.Message) error {
	var env domain.Envelope
	if err := json.Unmarshal(msg.Value, &env); err != nil {
		// Poison pill — log and skip rather than blocking the partition.
		c.log.Error("unmarshal envelope failed — skipping message (poison pill)",
			zap.Error(err),
			zap.ByteString("value", msg.Value[:min(len(msg.Value), 200)]),
		)
		return nil // returning nil commits the offset intentionally
	}

	c.log.Debug("event received",
		zap.String("type", string(env.Type)),
		zap.String("aggregate_id", env.AggregateID.String()),
		zap.Int64("offset", msg.Offset),
	)

	return c.dispatcher.Dispatch(ctx, env)
}

// sendToDLQ forwards the raw Kafka message to the dead-letter topic,
// annotated with the error reason and original topic metadata.
func (c *Consumer) sendToDLQ(ctx context.Context, msg kafkago.Message, reason error) {
	if c.dlqWriter == nil {
		return
	}
	meta, _ := json.Marshal(map[string]string{
		"original_topic":     msg.Topic,
		"original_partition": fmt.Sprintf("%d", msg.Partition),
		"original_offset":    fmt.Sprintf("%d", msg.Offset),
		"error":              reason.Error(),
		"failed_at":          time.Now().UTC().Format(time.RFC3339),
	})
	dlqMsg := kafkago.Message{
		Key:   msg.Key,
		Value: msg.Value,
		Headers: append(msg.Headers, kafkago.Header{
			Key:   "dlq-metadata",
			Value: meta,
		}),
	}
	if err := c.dlqWriter.WriteMessages(ctx, dlqMsg); err != nil {
		c.log.Error("failed to write message to DLQ", zap.Error(err))
	} else {
		c.log.Warn("message sent to DLQ",
			zap.Int64("offset", msg.Offset),
			zap.Int("partition", msg.Partition),
		)
	}
}

func (c *Consumer) Close() error {
	if c.dlqWriter != nil {
		_ = c.dlqWriter.Close()
	}
	return c.reader.Close()
}

func msgKey(msg kafkago.Message) string {
	return fmt.Sprintf("%d:%d", msg.Partition, msg.Offset)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
