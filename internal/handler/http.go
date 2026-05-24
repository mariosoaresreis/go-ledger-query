// Package handler contains the Gin HTTP handlers for the query service.
// Every route is read-only. No POST, PUT, or DELETE endpoints exist here.
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

type Handler struct {
	svc ports.QueryService
	log *zap.Logger
}

func New(svc ports.QueryService, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register wires all read routes onto the given router group.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.GET("/accounts/:id/balance",      h.GetBalance)
	rg.GET("/accounts/:id/entries",      h.ListEntries)
	rg.GET("/accounts/:id/transactions", h.ListTransactions)
	rg.GET("/transactions/:id",          h.GetTransaction)
}

// ── GET /v1/accounts/:id/balance ─────────────────────────────────────────────
// Response: 200 { account_id, owner_id, currency, balance, available }
//           404 account not found in projections
func (h *Handler) GetBalance(c *gin.Context) {
	accountID, err := parseUUID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errResp(err))
		return
	}
	v, err := h.svc.GetBalance(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(httpStatus(err), errResp(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"account_id": v.AccountID,
		"owner_id":   v.OwnerID,
		"currency":   v.Currency,
		"balance":    v.Balance.String(),
	})
}

// ── GET /v1/accounts/:id/entries?page=1&page_size=50 ─────────────────────────
// Response: 200 { items: [...], page, page_size }
func (h *Handler) ListEntries(c *gin.Context) {
	accountID, err := parseUUID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errResp(err))
		return
	}
	result, err := h.svc.ListEntries(c.Request.Context(), ports.ListEntriesQuery{
		AccountID: accountID,
		Page:      intQuery(c, "page", 1),
		PageSize:  intQuery(c, "page_size", 50),
	})
	if err != nil {
		c.JSON(httpStatus(err), errResp(err))
		return
	}
	c.JSON(http.StatusOK, result)
}

// ── GET /v1/transactions/:id ──────────────────────────────────────────────────
// Response: 200 { id, idempotency_key, debit_account_id, credit_account_id,
//                 amount, currency, description, status, created_at }
//           404 not found
func (h *Handler) GetTransaction(c *gin.Context) {
	txID, err := parseUUID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errResp(err))
		return
	}
	v, err := h.svc.GetTransaction(c.Request.Context(), txID)
	if err != nil {
		c.JSON(httpStatus(err), errResp(err))
		return
	}
	c.JSON(http.StatusOK, v)
}

// ── GET /v1/accounts/:id/transactions?page=1&page_size=50 ────────────────────
// Response: 200 { items: [...], page, page_size }
func (h *Handler) ListTransactions(c *gin.Context) {
	accountID, err := parseUUID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, errResp(err))
		return
	}
	result, err := h.svc.ListTransactions(c.Request.Context(), ports.ListTransactionsQuery{
		AccountID: accountID,
		Page:      intQuery(c, "page", 1),
		PageSize:  intQuery(c, "page_size", 50),
	})
	if err != nil {
		c.JSON(httpStatus(err), errResp(err))
		return
	}
	c.JSON(http.StatusOK, result)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func errResp(err error) gin.H { return gin.H{"error": err.Error()} }

// httpStatus maps domain error types to HTTP status codes.
// Everything that is not explicitly a not-found error is treated as 500
// so accidental data leakage through error messages is minimised.
func httpStatus(err error) int {
	if errors.Is(err, domain.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func parseUUID(c *gin.Context, param string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(param))
	if err != nil {
		return uuid.Nil, errors.New("invalid uuid: " + param)
	}
	return id, nil
}

func intQuery(c *gin.Context, key string, def int) int {
	v, err := strconv.Atoi(c.DefaultQuery(key, strconv.Itoa(def)))
	if err != nil || v <= 0 {
		return def
	}
	return v
}
