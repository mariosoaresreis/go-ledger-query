// Package domain holds the read-side value objects and the event contracts
// published by ledger-command over the ledger.transactions Kafka topic.
//
// ─── Contract rules ──────────────────────────────────────────────────────────
// Every struct here MUST match the JSON produced by ledger-command's domain
// package. Field names, json tags and types are intentionally identical.
// Any divergence breaks deserialization silently — verify against
// ledger-command/internal/domain/ledger.go before changing anything.
package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ── Shared value types ────────────────────────────────────────────────────────

type Currency string

const (
	USD Currency = "USD"
	EUR Currency = "EUR"
	BRL Currency = "BRL"
)

type EntryType string

const (
	EntryDebit    EntryType = "DEBIT"
	EntryCredit   EntryType = "CREDIT"
	EntryReversal EntryType = "REVERSAL"
)

type TxStatus string

const (
	TxPending  TxStatus = "PENDING"
	TxPosted   TxStatus = "POSTED"
	TxReversed TxStatus = "REVERSED"
	TxFailed   TxStatus = "FAILED"
)

// ── Kafka envelope ─────────────────────────────────────────────────────────────
// Matches ledger-command domain.Event exactly.

type EventType string

const (
	EventAccountCreated EventType = "account.created"
	EventTxPosted       EventType = "transaction.posted"
	EventTxReversed     EventType = "transaction.reversed"
	EventTxFailed       EventType = "transaction.failed"
)

// Envelope is the top-level message on the ledger.transactions topic.
// Payload is kept as raw JSON so each handler can lazily unmarshal into the
// concrete type it needs — one json.Unmarshal per message instead of two.
type Envelope struct {
	ID            uuid.UUID       `json:"id"`
	Type          EventType       `json:"type"`
	AggregateID   uuid.UUID       `json:"aggregate_id"`
	AggregateType string          `json:"aggregate_type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}

// ── Payload structs ───────────────────────────────────────────────────────────
// These mirror ledger-command AccountCreatedPayload, TxPostedPayload, etc.

type AccountCreatedPayload struct {
	AccountID uuid.UUID `json:"account_id"`
	OwnerID   uuid.UUID `json:"owner_id"`
	Currency  Currency  `json:"currency"`
}

type LedgerEntryPayload struct {
	ID            uuid.UUID       `json:"id"`
	AccountID     uuid.UUID       `json:"account_id"`
	TransactionID uuid.UUID       `json:"transaction_id"`
	Type          EntryType       `json:"type"`
	Amount        decimal.Decimal `json:"amount"`
	BalanceBefore decimal.Decimal `json:"balance_before"`
	BalanceAfter  decimal.Decimal `json:"balance_after"`
	Currency      Currency        `json:"currency"`
	Description   string          `json:"description"`
	CreatedAt     time.Time       `json:"created_at"`
}

type TxPostedPayload struct {
	TransactionID   uuid.UUID            `json:"transaction_id"`
	IdempotencyKey  string               `json:"idempotency_key"`
	DebitAccountID  uuid.UUID            `json:"debit_account_id"`
	CreditAccountID uuid.UUID            `json:"credit_account_id"`
	Amount          decimal.Decimal      `json:"amount"`
	Currency        Currency             `json:"currency"`
	Description     string               `json:"description"`
	Entries         []LedgerEntryPayload `json:"entries"`
}

type TxReversedPayload struct {
	OriginalTxID uuid.UUID `json:"original_tx_id"`
	ReversalTxID uuid.UUID `json:"reversal_tx_id"`
}

// ── Read-model views (projection outputs) ────────────────────────────────────

type BalanceView struct {
	AccountID uuid.UUID       `json:"account_id"`
	OwnerID   uuid.UUID       `json:"owner_id"`
	Currency  Currency        `json:"currency"`
	Balance   decimal.Decimal `json:"balance"`
}

type TransactionView struct {
	ID              uuid.UUID       `json:"id"`
	IdempotencyKey  string          `json:"idempotency_key"`
	DebitAccountID  uuid.UUID       `json:"debit_account_id"`
	CreditAccountID uuid.UUID       `json:"credit_account_id"`
	Amount          decimal.Decimal `json:"amount"`
	Currency        Currency        `json:"currency"`
	Description     string          `json:"description"`
	Status          TxStatus        `json:"status"`
	CreatedAt       time.Time       `json:"created_at"`
}

type LedgerEntryView struct {
	ID            uuid.UUID       `json:"id"`
	AccountID     uuid.UUID       `json:"account_id"`
	TransactionID uuid.UUID       `json:"transaction_id"`
	Type          EntryType       `json:"type"`
	Amount        decimal.Decimal `json:"amount"`
	BalanceBefore decimal.Decimal `json:"balance_before"`
	BalanceAfter  decimal.Decimal `json:"balance_after"`
	Currency      Currency        `json:"currency"`
	Description   string          `json:"description"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Pagination wraps any list result.
type Page[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}
