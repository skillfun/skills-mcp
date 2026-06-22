package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
)

func TestX402VerificationMiddlewareAcceptsValidProof(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("test-proof").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("bundle:1001"))

	router := newTestRouter(db)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle:1001"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "test-proof")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if payload["toolName"] != "bundle:1001" {
		t.Fatalf("toolName = %q, want %q", payload["toolName"], "bundle:1001")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestX402VerificationMiddlewareRejectsInvalidProof(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("invalid-proof").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}))

	router := newTestRouter(db)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle:1001"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "invalid-proof")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPaymentRequired)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestX402VerificationMiddlewareReturnsInternalErrorOnDBFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("test-proof").
		WillReturnError(sql.ErrConnDone)

	router := newTestRouter(db)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle:1001"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "test-proof")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestLookupAuthorizedToolNamesSupportsBundleGrants(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("bundle-proof").
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name"}).
				AddRow("bundle:1001").
				AddRow("bundle:1002"),
		)

	authorizedToolNames, err := LookupAuthorizedToolNames(context.Background(), db, "bundle-proof")
	if err != nil {
		t.Fatalf("LookupAuthorizedToolNames() error = %v", err)
	}

	if len(authorizedToolNames) != 2 {
		t.Fatalf("len(authorizedToolNames) = %d, want 2", len(authorizedToolNames))
	}

	if _, ok := authorizedToolNames["bundle:1002"]; !ok {
		t.Fatalf("authorized tool bundle:1002 missing: %#v", authorizedToolNames)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func newTestRouter(db *sql.DB) *gin.Engine {
	router := gin.New()
	router.POST("/", X402VerificationMiddleware(db), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"toolName": c.GetString("x402.toolName"),
		})
	})

	return router
}
