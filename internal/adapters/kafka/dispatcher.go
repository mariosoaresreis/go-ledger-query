// Package kafka — dispatcher and projection handlers.
package kafka

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"

	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

const tracerName = "ledger-query/kafka"

// Dispatcher implements ports.EventDispatcher.
// It unmarshals each envelope's raw JSON payload directly into the concrete
// struct (one json.Unmarshal per message, not two) and calls the matching
// projection handler on the ProjectionStore.
type Dispatcher struct {
	store ports.ProjectionStore
	log   *zap.Logger
}

func NewDispatcher(store ports.ProjectionStore, log *zap.Logger) *Dispatcher {
	return &Dispatcher{store: store, log: log}
}

// Dispatch routes an envelope to the right handler by event type.
// Unknown event types are silently skipped for forward-compatibility.
func (d *Dispatcher) Dispatch(ctx context.Context, env domain.Envelope) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "Dispatch")
	span.SetAttributes(
		attribute.String("event.type", string(env.Type)),
		attribute.String("event.aggregate_id", env.AggregateID.String()),
	)
	defer span.End()

	var err error
	switch env.Type {
	case domain.EventAccountCreated:
		err = d.onAccountCreated(ctx, env)
	case domain.EventTxPosted:
		err = d.onTxPosted(ctx, env)
	case domain.EventTxReversed:
		err = d.onTxReversed(ctx, env)
	case domain.EventTxFailed:
		// Failed transactions don't create projections — nothing to do.
	default:
		d.log.Debug("unknown event type — skipping", zap.String("type", string(env.Type)))
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (d *Dispatcher) onAccountCreated(ctx context.Context, env domain.Envelope) error {
	var p domain.AccountCreatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return fmt.Errorf("account.created payload: %w", err)
	}
	view := &domain.BalanceView{
		AccountID: p.AccountID,
		OwnerID:   p.OwnerID,
		Currency:  p.Currency,
	}
	if err := d.store.UpsertBalance(ctx, view); err != nil {
		return fmt.Errorf("upsert balance for account %s: %w", p.AccountID, err)
	}
	d.log.Info("projection: account created", zap.String("account_id", p.AccountID.String()))
	return nil
}

// onTxPosted materialises three projection tables atomically inside a single
// database transaction: one transaction_view row, N ledger_entry_view rows,
// and one balance_view row per unique account in the entry list.
// If any write fails the whole transaction rolls back — no partial projections.
func (d *Dispatcher) onTxPosted(ctx context.Context, env domain.Envelope) error {
	var p domain.TxPostedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return fmt.Errorf("transaction.posted payload: %w", err)
	}

	return d.store.RunInTx(ctx, func(tx ports.ReadRepository) error {
		txView := &domain.TransactionView{
			ID:              p.TransactionID,
			IdempotencyKey:  p.IdempotencyKey,
			DebitAccountID:  p.DebitAccountID,
			CreditAccountID: p.CreditAccountID,
			Amount:          p.Amount,
			Currency:        p.Currency,
			Description:     p.Description,
			Status:          domain.TxPosted,
			CreatedAt:       env.OccurredAt,
		}
		if err := tx.UpsertTransaction(ctx, txView); err != nil {
			return fmt.Errorf("upsert transaction view %s: %w", p.TransactionID, err)
		}

		for _, e := range p.Entries {
			if err := tx.UpsertEntry(ctx, &domain.LedgerEntryView{
				ID:            e.ID,
				AccountID:     e.AccountID,
				TransactionID: e.TransactionID,
				Type:          e.Type,
				Amount:        e.Amount,
				BalanceBefore: e.BalanceBefore,
				BalanceAfter:  e.BalanceAfter,
				Currency:      e.Currency,
				Description:   e.Description,
				CreatedAt:     e.CreatedAt,
			}); err != nil {
				return fmt.Errorf("upsert entry view %s: %w", e.ID, err)
			}

			if err := tx.UpsertBalance(ctx, &domain.BalanceView{
				AccountID: e.AccountID,
				Currency:  e.Currency,
				Balance:   e.BalanceAfter,
			}); err != nil {
				return fmt.Errorf("upsert balance for account %s: %w", e.AccountID, err)
			}
		}
		return nil
	})
}

// onTxReversed marks the original transaction as REVERSED in the projection.
func (d *Dispatcher) onTxReversed(ctx context.Context, env domain.Envelope) error {
	var p domain.TxReversedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return fmt.Errorf("transaction.reversed payload: %w", err)
	}

	existing, err := d.store.GetTransaction(ctx, p.OriginalTxID)
	if err != nil {
		return fmt.Errorf("get original tx view %s: %w", p.OriginalTxID, err)
	}

	existing.Status = domain.TxReversed
	if err := d.store.UpsertTransaction(ctx, existing); err != nil {
		return fmt.Errorf("upsert reversed tx view %s: %w", p.OriginalTxID, err)
	}

	d.log.Info("projection: transaction reversed",
		zap.String("original_id", p.OriginalTxID.String()),
		zap.String("reversal_id", p.ReversalTxID.String()),
	)
	return nil
}
