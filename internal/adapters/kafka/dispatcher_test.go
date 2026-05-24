package kafka_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	kafkaPkg "github.com/mariosoaresreis/go-ledger-query/internal/adapters/kafka"
	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/mocks"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

func envelope(t domain.EventType, payload interface{}) domain.Envelope {
	raw, _ := json.Marshal(payload)
	return domain.Envelope{
		ID:          uuid.New(),
		Type:        t,
		AggregateID: uuid.New(),
		OccurredAt:  time.Now(),
		Payload:     raw,
	}
}

func TestDispatch_AccountCreated(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := mocks.NewMockProjectionStore(ctrl)
	d := kafkaPkg.NewDispatcher(store, zap.NewNop())

	accountID := uuid.New()
	ownerID := uuid.New()

	store.EXPECT().UpsertBalance(gomock.Any(), gomock.Cond(func(v *domain.BalanceView) bool {
		return v.AccountID == accountID && v.OwnerID == ownerID && string(v.Currency) == "USD"
	})).Return(nil)

	env := envelope(domain.EventAccountCreated, map[string]interface{}{
		"account_id": accountID.String(),
		"owner_id":   ownerID.String(),
		"currency":   "USD",
	})
	require.NoError(t, d.Dispatch(context.Background(), env))
}

func TestDispatch_TxPosted(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := mocks.NewMockProjectionStore(ctrl)
	d := kafkaPkg.NewDispatcher(store, zap.NewNop())

	txID := uuid.New()
	debitAcc := uuid.New()
	creditAcc := uuid.New()
	entryID1 := uuid.New()
	entryID2 := uuid.New()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// RunInTx: capture and immediately execute the fn with the store itself
	store.EXPECT().RunInTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(ports.ReadRepository) error) error {
			return fn(store)
		},
	)
	store.EXPECT().UpsertTransaction(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().UpsertEntry(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	store.EXPECT().UpsertBalance(gomock.Any(), gomock.Any()).Return(nil).Times(2)

	env := envelope(domain.EventTxPosted, map[string]interface{}{
		"transaction_id":    txID.String(),
		"idempotency_key":   "idem-1",
		"debit_account_id":  debitAcc.String(),
		"credit_account_id": creditAcc.String(),
		"amount":            "100.00",
		"currency":          "USD",
		"description":       "test",
		"entries": []interface{}{
			map[string]interface{}{
				"id": entryID1.String(), "account_id": debitAcc.String(),
				"transaction_id": txID.String(), "type": "DEBIT",
				"amount": "100.00", "balance_before": "200.00", "balance_after": "100.00",
				"currency": "USD", "description": "test", "created_at": now,
			},
			map[string]interface{}{
				"id": entryID2.String(), "account_id": creditAcc.String(),
				"transaction_id": txID.String(), "type": "CREDIT",
				"amount": "100.00", "balance_before": "0.00", "balance_after": "100.00",
				"currency": "USD", "description": "test", "created_at": now,
			},
		},
	})
	require.NoError(t, d.Dispatch(context.Background(), env))
}

func TestDispatch_TxReversed(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := mocks.NewMockProjectionStore(ctrl)
	d := kafkaPkg.NewDispatcher(store, zap.NewNop())

	origTxID := uuid.New()
	reversalTxID := uuid.New()
	existing := &domain.TransactionView{
		ID:     origTxID,
		Status: domain.TxPosted,
		Amount: decimal.NewFromInt(50),
	}

	store.EXPECT().GetTransaction(gomock.Any(), origTxID).Return(existing, nil)
	store.EXPECT().UpsertTransaction(gomock.Any(), gomock.Cond(func(v *domain.TransactionView) bool {
		return v.Status == domain.TxReversed
	})).Return(nil)

	env := envelope(domain.EventTxReversed, map[string]interface{}{
		"original_tx_id": origTxID.String(),
		"reversal_tx_id": reversalTxID.String(),
	})
	require.NoError(t, d.Dispatch(context.Background(), env))
}

func TestDispatch_TxFailed_NoOp(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := mocks.NewMockProjectionStore(ctrl)
	d := kafkaPkg.NewDispatcher(store, zap.NewNop())
	env := envelope(domain.EventTxFailed, map[string]interface{}{})
	require.NoError(t, d.Dispatch(context.Background(), env))
}

func TestDispatch_UnknownEvent_Skipped(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := mocks.NewMockProjectionStore(ctrl)
	d := kafkaPkg.NewDispatcher(store, zap.NewNop())
	env := envelope("some.unknown.event", map[string]interface{}{})
	require.NoError(t, d.Dispatch(context.Background(), env))
}
