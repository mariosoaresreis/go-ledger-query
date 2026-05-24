// Package ports defines the hexagonal boundary for the query service.
// The application layer and consumer depend only on these interfaces.
package ports

import (
	"context"

	"github.com/google/uuid"

	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
)

// ── Inbound: HTTP → application ───────────────────────────────────────────────

// QueryService is the single inbound port for the HTTP handler.
// All read operations flow through here.
type QueryService interface {
	GetBalance(ctx context.Context, accountID uuid.UUID) (*domain.BalanceView, error)
	ListEntries(ctx context.Context, q ListEntriesQuery) (*domain.Page[domain.LedgerEntryView], error)
	GetTransaction(ctx context.Context, txID uuid.UUID) (*domain.TransactionView, error)
	ListTransactions(ctx context.Context, q ListTransactionsQuery) (*domain.Page[domain.TransactionView], error)
}

// ── Outbound: application → postgres ─────────────────────────────────────────

// ReadRepository is the persistence port for the query service.
// Implemented by the Postgres adapter against the read-projection schema.
type ReadRepository interface {
	// Projection writes — called by the Kafka consumer.
	UpsertBalance(ctx context.Context, v *domain.BalanceView) error
	UpsertTransaction(ctx context.Context, v *domain.TransactionView) error
	UpsertEntry(ctx context.Context, v *domain.LedgerEntryView) error

	// Query reads — called by the HTTP handler via QueryService.
	GetBalance(ctx context.Context, accountID uuid.UUID) (*domain.BalanceView, error)
	ListEntries(ctx context.Context, q ListEntriesQuery) ([]domain.LedgerEntryView, error)
	GetTransaction(ctx context.Context, txID uuid.UUID) (*domain.TransactionView, error)
	ListTransactions(ctx context.Context, q ListTransactionsQuery) ([]domain.TransactionView, error)
}

// Transactor wraps multiple repository operations in a single DB transaction.
// fn receives a ReadRepository scoped to the transaction; returning a non-nil
// error causes an automatic rollback.
type Transactor interface {
	RunInTx(ctx context.Context, fn func(tx ReadRepository) error) error
}

// ProjectionStore is the full outbound capability required by the Kafka
// dispatcher: atomic writes (Transactor) plus read access for event handlers
// that need to fetch existing projections (e.g. onTxReversed).
type ProjectionStore interface {
	ReadRepository
	Transactor
}

// ── Outbound: consumer → dispatcher ──────────────────────────────────────────

// EventDispatcher routes incoming Kafka envelopes to the right handler.
// Implemented by the Dispatcher in the kafka adapter.
type EventDispatcher interface {
	Dispatch(ctx context.Context, env domain.Envelope) error
}

// ── Query DTOs ────────────────────────────────────────────────────────────────

type ListEntriesQuery struct {
	AccountID uuid.UUID
	Page      int
	PageSize  int
}

type ListTransactionsQuery struct {
	AccountID uuid.UUID
	Page      int
	PageSize  int
}

func (q *ListEntriesQuery) Normalise() {
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.PageSize <= 0 || q.PageSize > 200 {
		q.PageSize = 50
	}
}

func (q *ListTransactionsQuery) Normalise() {
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.PageSize <= 0 || q.PageSize > 200 {
		q.PageSize = 50
	}
}
