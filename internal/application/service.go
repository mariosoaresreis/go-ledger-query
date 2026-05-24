// Package application implements the query-service use cases.
// It only imports domain and ports — never adapters or frameworks.
package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"

	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

const tracerName = "ledger-query/application"

// Service handles all read-only queries against the projection database.
// It is completely isolated from the write side — it never calls the
// command service and never touches the write schema.
type Service struct {
	repo ports.ReadRepository
	log  *zap.Logger
}

func New(repo ports.ReadRepository, log *zap.Logger) *Service {
	return &Service{repo: repo, log: log}
}

func (s *Service) GetBalance(ctx context.Context, accountID uuid.UUID) (*domain.BalanceView, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetBalance")
	span.SetAttributes(attribute.String("account.id", accountID.String()))
	defer span.End()

	v, err := s.repo.GetBalance(ctx, accountID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("get balance %s: %w", accountID, err)
	}
	return v, nil
}

func (s *Service) ListEntries(ctx context.Context, q ports.ListEntriesQuery) (*domain.Page[domain.LedgerEntryView], error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListEntries")
	span.SetAttributes(attribute.String("account.id", q.AccountID.String()))
	defer span.End()

	q.Normalise()
	items, err := s.repo.ListEntries(ctx, q)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("list entries: %w", err)
	}
	return &domain.Page[domain.LedgerEntryView]{Items: items, Page: q.Page, PageSize: q.PageSize}, nil
}

func (s *Service) GetTransaction(ctx context.Context, txID uuid.UUID) (*domain.TransactionView, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetTransaction")
	span.SetAttributes(attribute.String("transaction.id", txID.String()))
	defer span.End()

	v, err := s.repo.GetTransaction(ctx, txID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("get transaction %s: %w", txID, err)
	}
	return v, nil
}

func (s *Service) ListTransactions(ctx context.Context, q ports.ListTransactionsQuery) (*domain.Page[domain.TransactionView], error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListTransactions")
	span.SetAttributes(attribute.String("account.id", q.AccountID.String()))
	defer span.End()

	q.Normalise()
	items, err := s.repo.ListTransactions(ctx, q)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	return &domain.Page[domain.TransactionView]{Items: items, Page: q.Page, PageSize: q.PageSize}, nil
}
