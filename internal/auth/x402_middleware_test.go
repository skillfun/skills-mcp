package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
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

func TestX402VerificationMiddlewareRestoresRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("test-proof").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("bundle:1001"))

	router := newTestRouterWithHandler(db, func(c *gin.Context) {
		body, readErr := io.ReadAll(c.Request.Body)
		if readErr != nil {
			t.Fatalf("ReadAll() error = %v", readErr)
		}
		c.JSON(http.StatusOK, gin.H{
			"body":     string(body),
			"toolName": c.GetString("x402.toolName"),
		})
	})

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle:1001"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "test-proof")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"body":"{\"name\":\"bundle:1001\"}"`)) {
		t.Fatalf("body = %s", recorder.Body.String())
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

func TestX402VerificationMiddlewareRejectsInvalidJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newTestRouter(db)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "ignored")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
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

func TestX402VerificationMiddlewareRejectsNilDB(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/", X402VerificationMiddleware(nil), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle:1001"}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestX402VerificationMiddlewareReadsToolNameFromParams(t *testing.T) {
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
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"params":{"name":"bundle:1001"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "test-proof")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestX402VerificationMiddlewareRejectsBundleMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := gin.New()
	router.POST("/", func(c *gin.Context) {
		c.Set(RequestedBundleContextKey, "weather")
		c.Next()
	}, X402VerificationMiddleware(db), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"alerts:1001"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "test-proof")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestX402VerificationMiddlewareRequiresPaymentProof(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newTestRouter(db)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle:1001"}`))
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPaymentRequired)
	}

	var payload paymentRequiredResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload.Settlement["totalPrice"] != float64(basePriceSKILL+curatorMarkupSKILL) {
		t.Fatalf("settlement = %#v", payload.Settlement)
	}
}

func TestX402VerificationMiddlewareRejectsUnreadableBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newTestRouter(db)
	request := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(errReader{}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestX402VerificationMiddlewareRejectsInvalidToolNameFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newTestRouter(db)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestX402VerificationMiddlewareAllowsOpaqueToolNameWithinBundleURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := gin.New()
	router.POST("/", func(c *gin.Context) {
		c.Set(RequestedBundleContextKey, "weather")
		c.Next()
	}, X402VerificationMiddleware(db), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"current"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPaymentRequired)
	}
}

func TestX402VerificationMiddlewareAcceptsMatchingBundleToolWithinBundleURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("test-proof").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather:1001"))

	router := gin.New()
	router.POST("/", func(c *gin.Context) {
		c.Set(RequestedBundleContextKey, "weather")
		c.Next()
	}, X402VerificationMiddleware(db), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"toolName":   c.GetString("x402.toolName"),
			"bundleName": c.GetString("x402.bundleName"),
			"nftId":      c.GetString("x402.nftId"),
		})
	})

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"weather:1001"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(PaymentProofHeader, "test-proof")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestNewX402MiddlewareUsesVerificationMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/", NewX402Middleware(nil), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"bundle:1001"}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestExtractToolName(t *testing.T) {
	testCases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{name: "top level", body: `{"name":"bundle:1001"}`, want: "bundle:1001"},
		{name: "params", body: `{"params":{"name":"bundle:1002"}}`, want: "bundle:1002"},
		{name: "empty body", body: ``, wantErr: true},
		{name: "invalid json", body: `{`, wantErr: true},
		{name: "missing name", body: `{"params":{}}`, wantErr: true},
	}

	for _, testCase := range testCases {
		toolName, err := extractToolName([]byte(testCase.body))
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error", testCase.name)
			}
			continue
		}
		if err != nil || toolName != testCase.want {
			t.Fatalf("%s: extractToolName() = (%q, %v), want (%q, nil)", testCase.name, toolName, err, testCase.want)
		}
	}
}

func TestParseToolName(t *testing.T) {
	testCases := []struct {
		toolName   string
		wantBundle string
		wantNFTID  string
		wantErr    bool
	}{
		{toolName: "bundle:1001", wantBundle: "bundle", wantNFTID: "1001"},
		{toolName: "bundle/1002", wantBundle: "bundle", wantNFTID: "1002"},
		{toolName: "", wantErr: true},
		{toolName: "bundle:", wantErr: true},
		{toolName: "bundle", wantErr: true},
	}

	for _, testCase := range testCases {
		bundleName, nftID, err := parseToolName(testCase.toolName)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("parseToolName(%q) error = nil, want error", testCase.toolName)
			}
			continue
		}
		if err != nil || bundleName != testCase.wantBundle || nftID != testCase.wantNFTID {
			t.Fatalf("parseToolName(%q) = (%q, %q, %v)", testCase.toolName, bundleName, nftID, err)
		}
	}
}

func TestLookupAuthorizedToolNamesHandlesEmptyProof(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	authorizedToolNames, err := LookupAuthorizedToolNames(context.Background(), db, " ")
	if err != nil {
		t.Fatalf("LookupAuthorizedToolNames() error = %v", err)
	}
	if len(authorizedToolNames) != 0 {
		t.Fatalf("authorizedToolNames = %#v, want empty", authorizedToolNames)
	}
}

func TestLookupAuthorizedToolNamesRejectsNilDB(t *testing.T) {
	_, err := LookupAuthorizedToolNames(context.Background(), nil, "proof")
	if err == nil {
		t.Fatal("LookupAuthorizedToolNames() error = nil, want error")
	}
}

func TestLookupAuthorizedToolNamesPropagatesRowError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"tool_name"}).AddRow("bundle:1001").RowError(0, sql.ErrConnDone)
	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("proof").
		WillReturnRows(rows)

	_, err = LookupAuthorizedToolNames(context.Background(), db, "proof")
	if err == nil {
		t.Fatal("LookupAuthorizedToolNames() error = nil, want error")
	}
}

func TestLookupAuthorizedToolNamesPropagatesScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(authorizedToolsByProofQuery)).
		WithArgs("proof").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow(nil))

	_, err = LookupAuthorizedToolNames(context.Background(), db, "proof")
	if err == nil {
		t.Fatal("LookupAuthorizedToolNames() error = nil, want error")
	}
}

func newTestRouter(db *sql.DB) *gin.Engine {
	return newTestRouterWithHandler(db, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"toolName": c.GetString("x402.toolName"),
		})
	})
}

func newTestRouterWithHandler(db *sql.DB, handler gin.HandlerFunc) *gin.Engine {
	router := gin.New()
	router.POST("/", X402VerificationMiddleware(db), handler)

	return router
}

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
