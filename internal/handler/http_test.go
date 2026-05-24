package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/mariosoaresreis/go-ledger-query/internal/domain"
	"github.com/mariosoaresreis/go-ledger-query/internal/handler"
	"github.com/mariosoaresreis/go-ledger-query/internal/mocks"
	"github.com/mariosoaresreis/go-ledger-query/internal/ports"
)

func init() { gin.SetMode(gin.TestMode) }

func setupRouter(svc ports.QueryService) *gin.Engine {
	r := gin.New()
	h := handler.New(svc, zap.NewNop())
	h.Register(r.Group("/v1"))
	return r
}

// ── GetBalance ────────────────────────────────────────────────────────────────

func TestGetBalance_200(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	accountID := uuid.New()
	svc.EXPECT().GetBalance(gomock.Any(), accountID).Return(&domain.BalanceView{
		AccountID: accountID,
		OwnerID:   uuid.New(),
		Currency:  domain.EUR,
		Balance:   decimal.NewFromFloat(42.00),
	}, nil)

	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/accounts/"+accountID.String()+"/balance", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, accountID.String(), body["account_id"])
	assert.Equal(t, "42", body["balance"])
}

func TestGetBalance_InvalidUUID(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/accounts/not-a-uuid/balance", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetBalance_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	accountID := uuid.New()
	svc.EXPECT().GetBalance(gomock.Any(), accountID).Return(nil, domain.NewNotFoundError("balance", accountID.String()))

	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/accounts/"+accountID.String()+"/balance", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// ── GetTransaction ────────────────────────────────────────────────────────────

func TestGetTransaction_200(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	txID := uuid.New()
	svc.EXPECT().GetTransaction(gomock.Any(), txID).Return(&domain.TransactionView{
		ID:     txID,
		Status: domain.TxPosted,
		Amount: decimal.NewFromInt(100),
	}, nil)

	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/transactions/"+txID.String(), nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetTransaction_InvalidUUID(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/transactions/bad", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetTransaction_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	txID := uuid.New()
	svc.EXPECT().GetTransaction(gomock.Any(), txID).Return(nil, domain.NewNotFoundError("transaction", txID.String()))

	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/transactions/"+txID.String(), nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// ── ListEntries ───────────────────────────────────────────────────────────────

func TestListEntries_200(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	accountID := uuid.New()
	svc.EXPECT().ListEntries(gomock.Any(), gomock.Any()).Return(&domain.Page[domain.LedgerEntryView]{
		Items:    []domain.LedgerEntryView{{ID: uuid.New(), AccountID: accountID, CreatedAt: time.Now()}},
		Page:     1,
		PageSize: 50,
	}, nil)

	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/accounts/"+accountID.String()+"/entries", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestListEntries_InvalidUUID(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/accounts/bad-uuid/entries", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── ListTransactions ──────────────────────────────────────────────────────────

func TestListTransactions_200(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	accountID := uuid.New()
	svc.EXPECT().ListTransactions(gomock.Any(), gomock.Any()).Return(&domain.Page[domain.TransactionView]{
		Items:    []domain.TransactionView{},
		Page:     1,
		PageSize: 50,
	}, nil)

	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/accounts/"+accountID.String()+"/transactions", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestListTransactions_InvalidUUID(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockQueryService(ctrl)
	r := setupRouter(svc)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/accounts/bad-uuid/transactions", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

