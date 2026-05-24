package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/mariosoaresreis/go-ledger-query/internal/application"
	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/mocks"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

func newSvc(t *testing.T) (*application.Service, *mocks.MockReadRepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockReadRepository(ctrl)
	svc := application.New(repo, zap.NewNop())
	return svc, repo
}

func TestGetBalance_OK(t *testing.T) {
	svc, repo := newSvc(t)
	accountID := uuid.New()
	want := &domain.BalanceView{
		AccountID: accountID,
		OwnerID:   uuid.New(),
		Currency:  domain.USD,
		Balance:   decimal.NewFromFloat(100.50),
	}
	repo.EXPECT().GetBalance(gomock.Any(), accountID).Return(want, nil)

	got, err := svc.GetBalance(context.Background(), accountID)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestGetBalance_Error(t *testing.T) {
	svc, repo := newSvc(t)
	accountID := uuid.New()
	repo.EXPECT().GetBalance(gomock.Any(), accountID).Return(nil, errors.New("not found"))

	_, err := svc.GetBalance(context.Background(), accountID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), accountID.String())
}

func TestListEntries_NormalisesPage(t *testing.T) {
	svc, repo := newSvc(t)
	accountID := uuid.New()
	// Page=0 should be normalised to 1, PageSize=0 to 50
	repo.EXPECT().ListEntries(gomock.Any(), ports.ListEntriesQuery{
		AccountID: accountID, Page: 1, PageSize: 50,
	}).Return(nil, nil)

	page, err := svc.ListEntries(context.Background(), ports.ListEntriesQuery{
		AccountID: accountID, Page: 0, PageSize: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, page.Page)
	assert.Equal(t, 50, page.PageSize)
}

func TestListEntries_ReturnsItems(t *testing.T) {
	svc, repo := newSvc(t)
	accountID := uuid.New()
	items := []domain.LedgerEntryView{
		{ID: uuid.New(), AccountID: accountID, Amount: decimal.NewFromInt(10), CreatedAt: time.Now()},
	}
	repo.EXPECT().ListEntries(gomock.Any(), gomock.Any()).Return(items, nil)

	page, err := svc.ListEntries(context.Background(), ports.ListEntriesQuery{AccountID: accountID, Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestGetTransaction_OK(t *testing.T) {
	svc, repo := newSvc(t)
	txID := uuid.New()
	want := &domain.TransactionView{ID: txID, Status: domain.TxPosted, Amount: decimal.NewFromInt(50)}
	repo.EXPECT().GetTransaction(gomock.Any(), txID).Return(want, nil)

	got, err := svc.GetTransaction(context.Background(), txID)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestGetTransaction_Error(t *testing.T) {
	svc, repo := newSvc(t)
	txID := uuid.New()
	repo.EXPECT().GetTransaction(gomock.Any(), txID).Return(nil, errors.New("not found"))

	_, err := svc.GetTransaction(context.Background(), txID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), txID.String())
}

func TestListTransactions_NormalisesPageSize(t *testing.T) {
	svc, repo := newSvc(t)
	accountID := uuid.New()
	// PageSize > 200 should be clamped to 50
	repo.EXPECT().ListTransactions(gomock.Any(), ports.ListTransactionsQuery{
		AccountID: accountID, Page: 1, PageSize: 50,
	}).Return(nil, nil)

	page, err := svc.ListTransactions(context.Background(), ports.ListTransactionsQuery{
		AccountID: accountID, Page: 1, PageSize: 999,
	})
	require.NoError(t, err)
	assert.Equal(t, 50, page.PageSize)
}

