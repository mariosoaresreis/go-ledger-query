//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	pgAdapter "github.com/mariosoaresreis/go-ledger-query/internal/adapters/postgres"
	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)
var testRepo *pgAdapter.Repo

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := tcpostgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:16-alpine"),
		tcpostgres.WithDatabase("ledger_test"),
		tcpostgres.WithUsername("ledger"),
		tcpostgres.WithPassword("secret"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		panic("start postgres container: " + err.Error())
	}
	defer pgContainer.Terminate(ctx) //nolint:errcheck

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("get connection string: " + err.Error())
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("connect pool: " + err.Error())
	}
	defer pool.Close()

	// Apply migrations
	sql, err := os.ReadFile("../../../migrations/001_init.sql")
	if err != nil {
		panic("read migration: " + err.Error())
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		panic("run migration: " + err.Error())
	}

	testRepo = pgAdapter.New(pool)
	os.Exit(m.Run())
}

// ── UpsertBalance / GetBalance ────────────────────────────────────────────────

func TestUpsertAndGetBalance(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()
	ownerID := uuid.New()

	require.NoError(t, testRepo.UpsertBalance(ctx, &domain.BalanceView{
		AccountID: accountID,
		OwnerID:   ownerID,
		Currency:  domain.USD,
		Balance:   decimal.NewFromFloat(123.45),
	}))

	got, err := testRepo.GetBalance(ctx, accountID)
	require.NoError(t, err)
	assert.Equal(t, accountID, got.AccountID)
	assert.Equal(t, ownerID, got.OwnerID)
	assert.Equal(t, domain.USD, got.Currency)
	assert.True(t, got.Balance.Equal(decimal.NewFromFloat(123.45)))
}

func TestUpsertBalance_UpdatesBalance(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()

	require.NoError(t, testRepo.UpsertBalance(ctx, &domain.BalanceView{
		AccountID: accountID, OwnerID: uuid.New(), Currency: domain.USD,
		Balance: decimal.NewFromInt(100),
	}))
	require.NoError(t, testRepo.UpsertBalance(ctx, &domain.BalanceView{
		AccountID: accountID, Currency: domain.USD,
		Balance: decimal.NewFromInt(200),
	}))

	got, err := testRepo.GetBalance(ctx, accountID)
	require.NoError(t, err)
	assert.True(t, got.Balance.Equal(decimal.NewFromInt(200)))
}

func TestGetBalance_NotFound(t *testing.T) {
	_, err := testRepo.GetBalance(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// ── UpsertTransaction / GetTransaction ───────────────────────────────────────

func TestUpsertAndGetTransaction(t *testing.T) {
	ctx := context.Background()
	txID := uuid.New()
	view := &domain.TransactionView{
		ID:              txID,
		IdempotencyKey:  "idem-" + txID.String(),
		DebitAccountID:  uuid.New(),
		CreditAccountID: uuid.New(),
		Amount:          decimal.NewFromFloat(50.00),
		Currency:        domain.EUR,
		Description:     "integration test",
		Status:          domain.TxPosted,
		CreatedAt:       time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, testRepo.UpsertTransaction(ctx, view))

	got, err := testRepo.GetTransaction(ctx, txID)
	require.NoError(t, err)
	assert.Equal(t, txID, got.ID)
	assert.Equal(t, domain.TxPosted, got.Status)
	assert.True(t, got.Amount.Equal(decimal.NewFromFloat(50.00)))
}

func TestUpsertTransaction_UpdatesStatus(t *testing.T) {
	ctx := context.Background()
	txID := uuid.New()
	view := &domain.TransactionView{
		ID: txID, IdempotencyKey: "k", DebitAccountID: uuid.New(),
		CreditAccountID: uuid.New(), Amount: decimal.NewFromInt(10),
		Currency: domain.USD, Status: domain.TxPosted,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, testRepo.UpsertTransaction(ctx, view))

	view.Status = domain.TxReversed
	require.NoError(t, testRepo.UpsertTransaction(ctx, view))

	got, err := testRepo.GetTransaction(ctx, txID)
	require.NoError(t, err)
	assert.Equal(t, domain.TxReversed, got.Status)
}

func TestGetTransaction_NotFound(t *testing.T) {
	_, err := testRepo.GetTransaction(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// ── UpsertEntry / ListEntries ─────────────────────────────────────────────────

func TestUpsertEntryAndListEntries(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()
	txID := uuid.New()

	entry := &domain.LedgerEntryView{
		ID:            uuid.New(),
		AccountID:     accountID,
		TransactionID: txID,
		Type:          domain.EntryDebit,
		Amount:        decimal.NewFromInt(30),
		BalanceBefore: decimal.NewFromInt(100),
		BalanceAfter:  decimal.NewFromInt(70),
		Currency:      domain.USD,
		Description:   "test entry",
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, testRepo.UpsertEntry(ctx, entry))

	page, err := testRepo.ListEntries(ctx, ports.ListEntriesQuery{
		AccountID: accountID, Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, entry.ID, page[0].ID)
	assert.True(t, page[0].Amount.Equal(decimal.NewFromInt(30)))
}

func TestUpsertEntry_IdempotentDoNothing(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()
	entry := &domain.LedgerEntryView{
		ID: uuid.New(), AccountID: accountID, TransactionID: uuid.New(),
		Type: domain.EntryCredit, Amount: decimal.NewFromInt(5),
		BalanceBefore: decimal.Zero, BalanceAfter: decimal.NewFromInt(5),
		Currency: domain.USD, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, testRepo.UpsertEntry(ctx, entry))
	// second insert must not error (DO NOTHING)
	require.NoError(t, testRepo.UpsertEntry(ctx, entry))
}

// ── ListTransactions ──────────────────────────────────────────────────────────

func TestListTransactions(t *testing.T) {
	ctx := context.Background()
	debitAcc := uuid.New()
	creditAcc := uuid.New()

	for i := 0; i < 3; i++ {
		v := &domain.TransactionView{
			ID: uuid.New(), IdempotencyKey: uuid.NewString(),
			DebitAccountID: debitAcc, CreditAccountID: creditAcc,
			Amount: decimal.NewFromInt(int64(i + 1)), Currency: domain.USD,
			Status:    domain.TxPosted,
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		}
		require.NoError(t, testRepo.UpsertTransaction(ctx, v))
	}

	rows, err := testRepo.ListTransactions(ctx, ports.ListTransactionsQuery{
		AccountID: debitAcc, Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(rows), 3)
}

