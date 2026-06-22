package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"skillfun-mcp/internal/auth"
	"skillfun-mcp/internal/mcp"
)

func TestListBundlesDoesNotRequireAuth(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT b.bundle_name, b.subdomain, b.display_name, .*FROM bundles b.*WHERE b.is_active = TRUE.*`).
		WillReturnRows(
			sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active", "skill_count"}).
				AddRow("weather", "wx", "weather", "weather bundle", true, 2),
		)

	router := newEngine(db, mcp.NewSchemaAggregator(db))
	request := httptest.NewRequest(http.MethodGet, "/v1/mcp/bundles", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBundleManagementRequiresAuth(t *testing.T) {
	t.Setenv("BUNDLE_ADMIN_TOKEN", "secret-token")

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/mcp/bundles",
		bytes.NewBufferString(`{"bundleName":"weather","displayName":"Weather","toolNames":["bundle:weather"]}`),
	)
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestBundleManagementCreatesBundleWithSkills(t *testing.T) {
	t.Setenv("BUNDLE_ADMIN_TOKEN", "secret-token")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT EXISTS \(\s*SELECT 1\s*FROM bundles\s*WHERE subdomain = \$1\s*\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`(?s)INSERT INTO bundles .*ON CONFLICT.*`).
		WithArgs("weather", sqlmock.AnyArg(), "Weather Bundle", "Weather tools", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)INSERT INTO skills .*ON CONFLICT.*`).
		WithArgs(
			int64(1001),
			"weather.current",
			sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT tool_name\s+FROM skills.*tool_name = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
	mock.ExpectExec(`(?s)DELETE FROM bundle_skills.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)INSERT INTO bundle_skills .*`).
		WithArgs("weather", "weather.current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(
			sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "wx7abcde9f", "Weather Bundle", "Weather tools", true),
		)

	router := newEngine(db, mcp.NewSchemaAggregator(db))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/mcp/bundles",
		bytes.NewBufferString(`{"bundleName":"weather","displayName":"Weather Bundle","description":"Weather tools","skills":[{"nftId":1001,"name":"weather.current","description":"Get current weather","inputSchema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret-token")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBundleManagementRejectsInvalidSubdomain(t *testing.T) {
	t.Setenv("BUNDLE_ADMIN_TOKEN", "secret-token")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectRollback()

	router := newEngine(db, mcp.NewSchemaAggregator(db))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/mcp/bundles",
		bytes.NewBufferString(`{"bundleName":"weather","subdomain":"bad","displayName":"Weather Bundle","description":"Weather tools","skills":[{"nftId":1001,"name":"weather.current","description":"Get current weather","inputSchema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret-token")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAgentFlowDiscoversBundleSkill(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)WITH active_grants AS .*FROM payment_proofs.*WHERE proof = \$1.*`).
		WithArgs("proof-agent").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("current"))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, .* FROM skills s JOIN bundle_skills.*b.subdomain = \$2.*tool_name = ANY\(\$3\).*LIMIT \$4`).
		WithArgs("%shanghai%", "wx", sqlmock.AnyArg(), mcp.DefaultSemanticLimit).
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name", "score"}).
				AddRow("current", 12),
		)
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.schema_json\s+FROM skills s.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name", "schema_json"}).
				AddRow("current", `{"name":"current","description":"Get weather for a city","inputSchema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}`),
		)

	router := newEngine(db, mcp.NewSchemaAggregator(db))
	discoveredTool := discoverToolForTask(t, router, "proof-agent", "wx", "Shanghai weather today")
	if discoveredTool.Name != "current" {
		t.Fatalf("discovered tool = %q, want %q", discoveredTool.Name, "current")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestPathBasedBundleURLListsOnlyBundleTools(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT s.tool_name, .* FROM skills s JOIN bundle_skills.*b.subdomain = \$2.*LIMIT \$3`).
		WithArgs("%weather%", "wx", mcp.DefaultSemanticLimit).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "score"}).AddRow("weather.current", 9))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.schema_json\s+FROM skills s.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name", "schema_json"}).
				AddRow("weather.current", `{"name":"weather.current","description":"Get weather","inputSchema":{"type":"object"}}`),
		)

	router := newEngine(db, mcp.NewSchemaAggregator(db))
	request := httptest.NewRequest(http.MethodGet, "/wx/tools?cursor_context=weather", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response mcp.ToolsListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(response.Tools) != 1 || response.Tools[0].Name != "weather.current" {
		t.Fatalf("unexpected tools response: %#v", response.Tools)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestSubdomainBundleURLDoesNotExposeToolsCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db))
	request := httptest.NewRequest(
		http.MethodPost,
		"/tools/call",
		bytes.NewBufferString(`{"name":"current","arguments":{"city":"Shanghai"}}`),
	)
	request.Host = "wx.skillfun.ai"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.PaymentProofHeader, "proof-host")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func discoverToolForTask(t *testing.T, router http.Handler, proof string, bundleName string, task string) mcp.MCPTool {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/"+bundleName+"/tools?cursor_context=Shanghai", nil)
	request.Header.Set(auth.PaymentProofHeader, proof)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response mcp.ToolsListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(response.Tools) == 0 {
		t.Fatalf("no tools discovered for task %q", task)
	}

	for _, tool := range response.Tools {
		if bytes.Contains(bytes.ToLower([]byte(tool.Description)), bytes.ToLower([]byte("weather"))) {
			return tool
		}
	}

	return response.Tools[0]
}
