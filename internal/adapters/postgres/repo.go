// Package postgres implements ports.ReadRepository for the query service.
// It operates on the read-projection schema — never touches the command schema.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx, allowing Repo to work
// inside or outside a database transaction without any code duplication.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Repo is the Postgres read-projection adapter.
type Repo struct {
	pool *pgxpool.Pool // kept for transaction management
	db   DBTX          // pool or an in-flight pgx.Tx
}

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool, db: pool} }

// RunInTx executes fn inside a single database transaction.
// If fn returns an error the transaction is rolled back automatically.
// Implements ports.Transactor.
func (r *Repo) RunInTx(ctx context.Context, fn func(tx ports.ReadRepository) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txRepo := &Repo{pool: r.pool, db: tx}
	if err := fn(txRepo); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// ── Projection writes (called by the Kafka dispatcher) ────────────────────────

// UpsertBalance inserts or updates the projected balance for an account.
// OwnerID is only set on INSERT (account.created event) so subsequent
// transaction.posted events don't clobber it.
func (r *Repo) UpsertBalance(ctx context.Context, v *domain.BalanceView) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO balance_views (account_id, owner_id, currency, balance)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id) DO UPDATE
		  SET balance   = EXCLUDED.balance,
		      updated_at = NOW()
		  WHERE EXCLUDED.balance IS NOT NULL`,
		v.AccountID, v.OwnerID, string(v.Currency), v.Balance.String(),
	)
	return err
}

// UpsertTransaction inserts or updates a transaction projection.
// The ON CONFLICT clause allows status updates (e.g. POSTED → REVERSED).
func (r *Repo) UpsertTransaction(ctx context.Context, v *domain.TransactionView) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO transaction_views
		  (id, idempotency_key, debit_account_id, credit_account_id,
		   amount, currency, description, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE
		  SET status     = EXCLUDED.status,
		      updated_at = NOW()`,
		v.ID, v.IdempotencyKey, v.DebitAccountID, v.CreditAccountID,
		v.Amount.String(), string(v.Currency), v.Description, string(v.Status), v.CreatedAt,
	)
	return err
}

// UpsertEntry inserts a ledger entry projection. Entries are immutable once
// created so DO NOTHING on conflict is correct.
func (r *Repo) UpsertEntry(ctx context.Context, v *domain.LedgerEntryView) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO entry_views
		  (id, account_id, transaction_id, type,
		   amount, balance_before, balance_after, currency, description, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO NOTHING`,
		v.ID, v.AccountID, v.TransactionID, string(v.Type),
		v.Amount.String(), v.BalanceBefore.String(), v.BalanceAfter.String(),
		string(v.Currency), v.Description, v.CreatedAt,
	)
	return err
}

// ── Query reads (called by the HTTP handler via QueryService) ─────────────────

func (r *Repo) GetBalance(ctx context.Context, accountID uuid.UUID) (*domain.BalanceView, error) {
	row := r.db.QueryRow(ctx, `
		SELECT account_id, owner_id, currency, balance
		FROM   balance_views
		WHERE  account_id = $1`, accountID)

	var v domain.BalanceView
	var balStr string
	if err := row.Scan(&v.AccountID, &v.OwnerID, &v.Currency, &balStr); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFoundError("balance", accountID.String())
		}
		return nil, err
	}
	v.Balance, _ = decimal.NewFromString(balStr)
	return &v, nil
}

func (r *Repo) ListEntries(ctx context.Context, q ports.ListEntriesQuery) ([]domain.LedgerEntryView, error) {
	offset := (q.Page - 1) * q.PageSize
	rows, err := r.db.Query(ctx, `
		SELECT id, account_id, transaction_id, type,
		       amount, balance_before, balance_after, currency, description, created_at
		FROM   entry_views
		WHERE  account_id = $1
		ORDER  BY created_at DESC
		LIMIT  $2 OFFSET $3`,
		q.AccountID, q.PageSize, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.LedgerEntryView
	for rows.Next() {
		var e domain.LedgerEntryView
		var amt, balB, balA string
		if err := rows.Scan(
			&e.ID, &e.AccountID, &e.TransactionID, &e.Type,
			&amt, &balB, &balA, &e.Currency, &e.Description, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		e.Amount, _ = decimal.NewFromString(amt)
		e.BalanceBefore, _ = decimal.NewFromString(balB)
		e.BalanceAfter, _ = decimal.NewFromString(balA)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repo) GetTransaction(ctx context.Context, txID uuid.UUID) (*domain.TransactionView, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, idempotency_key, debit_account_id, credit_account_id,
		       amount, currency, description, status, created_at
		FROM   transaction_views
		WHERE  id = $1`, txID)

	var v domain.TransactionView
	var amtStr string
	if err := row.Scan(
		&v.ID, &v.IdempotencyKey, &v.DebitAccountID, &v.CreditAccountID,
		&amtStr, &v.Currency, &v.Description, &v.Status, &v.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFoundError("transaction", txID.String())
		}
		return nil, err
	}
	v.Amount, _ = decimal.NewFromString(amtStr)
	return &v, nil
}

func (r *Repo) ListTransactions(ctx context.Context, q ports.ListTransactionsQuery) ([]domain.TransactionView, error) {
	offset := (q.Page - 1) * q.PageSize
	rows, err := r.db.Query(ctx, `
		SELECT id, idempotency_key, debit_account_id, credit_account_id,
		       amount, currency, description, status, created_at
		FROM   transaction_views
		WHERE  debit_account_id = $1
		    OR credit_account_id = $1
		ORDER  BY created_at DESC
		LIMIT  $2 OFFSET $3`,
		q.AccountID, q.PageSize, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.TransactionView
	for rows.Next() {
		var v domain.TransactionView
		var amtStr string
		if err := rows.Scan(
			&v.ID, &v.IdempotencyKey, &v.DebitAccountID, &v.CreditAccountID,
			&amtStr, &v.Currency, &v.Description, &v.Status, &v.CreatedAt,
		); err != nil {
			return nil, err
		}
		v.Amount, _ = decimal.NewFromString(amtStr)
		out = append(out, v)
	}
	return out, rows.Err()
}
