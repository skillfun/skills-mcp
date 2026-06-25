package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"

	"skillfun-mcp/internal/auth"
	bundlepkg "skillfun-mcp/internal/bundle"
	"skillfun-mcp/internal/mcp"
	"skillfun-mcp/internal/skills"
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

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
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

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
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
	mock.ExpectQuery(`(?s)SELECT nft_id, tool_name, COALESCE\(skill_dir_name, ''\), COALESCE\(sync_status, ''\), schema_json, COALESCE\(github_url, ''\)\s+FROM skills.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
	mock.ExpectQuery(`(?s)SELECT nft_id, COALESCE\(skill_dir_name, ''\)\s+FROM skills.*`).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}))
	mock.ExpectExec(`(?s)INSERT INTO skills .*ON CONFLICT.*`).
		WithArgs(
			int64(1001),
			"weather.current",
			sqlmock.AnyArg(),
			"https://github.com/example/weather-skill/tree/main/skills/current",
			"weather.current",
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
	mock.ExpectExec(`(?s)UPDATE skills\s+SET sync_status = 'ready'.*`).
		WithArgs(int64(1001)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(
			sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "wx7abcde9f", "Weather Bundle", "Weather tools", true),
		)

	router := newEngine(
		db,
		mcp.NewSchemaAggregator(db),
		bundlepkg.NewStore(db, bundlepkg.WithSkillSyncer(stubSkillStorage{})),
		stubSkillStorage{},
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/mcp/bundles",
		bytes.NewBufferString(`{"bundleName":"weather","displayName":"Weather Bundle","description":"Weather tools","skills":[{"nftId":1001,"name":"weather.current","description":"Get current weather","inputSchema":{"type":"object","properties":{"city":{"type":"string"}}},"githubUrl":"https://github.com/example/weather-skill/tree/main/skills/current"}]}`),
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

	router := newEngine(
		db,
		mcp.NewSchemaAggregator(db),
		bundlepkg.NewStore(db, bundlepkg.WithSkillSyncer(stubSkillStorage{})),
		stubSkillStorage{},
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/mcp/bundles",
		bytes.NewBufferString(`{"bundleName":"weather","subdomain":"bad","displayName":"Weather Bundle","description":"Weather tools","skills":[{"nftId":1001,"name":"weather.current","description":"Get current weather","inputSchema":{"type":"object","properties":{"city":{"type":"string"}}},"githubUrl":"https://github.com/example/weather-skill/tree/main/skills/current"}]}`),
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

func TestBundleManagementRejectsInvalidGitHubURL(t *testing.T) {
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
	mock.ExpectExec(`(?s)INSERT INTO bundles .*ON CONFLICT.*`).
		WithArgs("weather", "weatherhub", "Weather Bundle", "Weather tools", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT nft_id, tool_name, COALESCE\(skill_dir_name, ''\), COALESCE\(sync_status, ''\), schema_json, COALESCE\(github_url, ''\)\s+FROM skills.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
	mock.ExpectQuery(`(?s)SELECT nft_id, COALESCE\(skill_dir_name, ''\)\s+FROM skills.*`).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}))
	mock.ExpectRollback()

	router := newEngine(
		db,
		mcp.NewSchemaAggregator(db),
		bundlepkg.NewStore(db, bundlepkg.WithSkillSyncer(stubSkillStorage{})),
		stubSkillStorage{},
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/mcp/bundles",
		bytes.NewBufferString(`{"bundleName":"weather","subdomain":"weatherhub","displayName":"Weather Bundle","description":"Weather tools","skills":[{"nftId":1001,"name":"weather.current","description":"Get current weather","inputSchema":{"type":"object","properties":{"city":{"type":"string"}}},"githubUrl":"https://example.com/weather"}]}`),
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

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
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

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
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

func TestPathBasedBundleMCPListsResources(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("wx").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "wx", "Weather", "bundle", true))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.skill_dir_name\s+FROM bundle_skills bs.*`).
		WithArgs("wx").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).
			AddRow("current", "weather-current"))

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response struct {
		Result mcp.ResourcesListResponse `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(response.Result.Resources) != 1 || response.Result.Resources[0].URI != skills.BuildResourceURI("current", "prompt.md") {
		t.Fatalf("unexpected resources response: %#v", response.Result.Resources)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestPathBasedBundleMCPReadsResource(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("wx").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "wx", "Weather", "bundle", true))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.skill_dir_name\s+FROM bundle_skills bs.*`).
		WithArgs("wx").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).
			AddRow("current", "weather-current"))

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(
		http.MethodPost,
		"/wx/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response struct {
		Result mcp.ResourcesReadResponse `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(response.Result.Contents) != 1 || response.Result.Contents[0].Text != "# current" {
		t.Fatalf("unexpected read response: %#v", response.Result.Contents)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestPathBasedBundleMCPInitialize(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response struct {
		Result mcpInitializeResult `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Result.ProtocolVersion == "" || response.Result.ServerInfo.Name == "" {
		t.Fatalf("unexpected initialize response: %#v", response.Result)
	}
}

func TestPathBasedBundleMCPInitializedNotification(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", recorder.Body.String())
	}
}

func TestSubdomainBundleURLDoesNotExposeToolsCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
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

func TestHostBasedBundleURLListsOnlyBundleTools(t *testing.T) {
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

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodGet, "/tools?cursor_context=weather", nil)
	request.Host = "wx.skillfun.ai"
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

func TestHostBasedBundleURLRejectsUnknownHost(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodGet, "/tools", nil)
	request.Host = "mcp.skillfun.ai"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestPathBasedBundleMCPListsTools(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT s.tool_name, .* FROM skills s JOIN bundle_skills.*b.subdomain = \$2.*LIMIT \$3`).
		WithArgs("%weather%", "wx", 5).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "score"}).AddRow("weather.current", 9))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.schema_json\s+FROM skills s.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name", "schema_json"}).
				AddRow("weather.current", `{"name":"weather.current","description":"Get weather","inputSchema":{"type":"object"}}`),
		)

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursorContext":"weather","limit":5}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response mcpRPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected rpc error: %#v", response.Error)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestPathBasedBundleMCPRejectsInvalidJSONRPCVersion(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"1.0","id":1,"method":"initialize"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestPathBasedBundleMCPRejectsInvalidToolsListParams(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":"bad"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestPathBasedBundleMCPReturnsMethodNotFound(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
}

func TestPathBasedBundleMCPReturnsResourceNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("wx").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "wx", "Weather", "bundle", true))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.skill_dir_name\s+FROM bundle_skills bs.*`).
		WithArgs("wx").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).
			AddRow("current", "weather-current"))

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), errorSkillStorage{readErr: skills.ErrResourceNotFound})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestPathBasedBundleMCPReturnsPermissionLookupFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)WITH active_grants AS .*FROM payment_proofs.*WHERE proof = \$1.*`).
		WithArgs("broken-proof").
		WillReturnError(sql.ErrConnDone)

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.PaymentProofHeader, "broken-proof")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDeactivateBundleRoute(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	t.Setenv("BUNDLE_ADMIN_TOKEN", "secret")
	mock.ExpectExec(`(?s)UPDATE bundles\s+SET is_active = FALSE.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 1))

	router := newEngine(db, mcp.NewSchemaAggregator(db), bundlepkg.NewStore(db), stubSkillStorage{})
	request := httptest.NewRequest(http.MethodDelete, "/v1/mcp/bundles/weather", nil)
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestParseBundleNameFromHost(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		host string
		want string
	}{
		{host: "", want: ""},
		{host: " wx.skillfun.ai ", want: "wx"},
		{host: "wx.skillfun.ai", want: "wx"},
		{host: "wx.skillfun.ai:8080", want: "wx"},
		{host: "mcp.skillfun.ai", want: ""},
		{host: "a.b.skillfun.ai", want: ""},
		{host: "example.com", want: ""},
	}

	for _, testCase := range testCases {
		if got := parseBundleNameFromHost(testCase.host); got != testCase.want {
			t.Fatalf("parseBundleNameFromHost(%q) = %q, want %q", testCase.host, got, testCase.want)
		}
	}
}

func TestNewPostgresDBRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	db, err := newPostgresDB()
	if err == nil || db != nil {
		t.Fatalf("newPostgresDB() = (%v, %v), want missing env error", db, err)
	}
}

func TestPingPostgresReturnsErrorForClosedDB(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://invalid")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := pingPostgres(db); err == nil {
		t.Fatal("pingPostgres() error = nil, want error")
	}
}

type fatalPanic struct {
	message string
}

type fakeGatewayServer struct {
	listenErr      error
	shutdownErr    error
	listenStarted  chan struct{}
	shutdownCalled bool
}

type stubAggregator struct {
	tools       []mcp.MCPTool
	err         error
	lastOptions mcp.ListToolsOptions
}

func (aggregator *stubAggregator) GetAggregateTools(_ context.Context, options mcp.ListToolsOptions) ([]mcp.MCPTool, error) {
	aggregator.lastOptions = options
	if aggregator.err != nil {
		return nil, aggregator.err
	}
	return aggregator.tools, nil
}

type stubBundleStore struct {
	listActiveBundles       []bundlepkg.Bundle
	listActiveBundlesErr    error
	getBundleToolsResp      bundlepkg.BundleToolsResponse
	getBundleToolsErr       error
	upsertBundleResp        bundlepkg.Bundle
	upsertBundleErr         error
	deactivateErr           error
	resourceBindings        []bundlepkg.SkillResourceBinding
	listResourceBindingsErr error
	resourceBinding         bundlepkg.SkillResourceBinding
	getResourceBindingErr   error
	lastUpsert              bundlepkg.UpsertBundleInput
}

func (store *stubBundleStore) ListActiveBundles(context.Context) ([]bundlepkg.Bundle, error) {
	return store.listActiveBundles, store.listActiveBundlesErr
}

func (store *stubBundleStore) GetBundleTools(context.Context, string) (bundlepkg.BundleToolsResponse, error) {
	return store.getBundleToolsResp, store.getBundleToolsErr
}

func (store *stubBundleStore) UpsertBundle(_ context.Context, input bundlepkg.UpsertBundleInput) (bundlepkg.Bundle, error) {
	store.lastUpsert = input
	return store.upsertBundleResp, store.upsertBundleErr
}

func (store *stubBundleStore) DeactivateBundle(context.Context, string) error {
	return store.deactivateErr
}

func (store *stubBundleStore) ListBundleResourceBindings(context.Context, string, map[string]struct{}) ([]bundlepkg.SkillResourceBinding, error) {
	return store.resourceBindings, store.listResourceBindingsErr
}

func (store *stubBundleStore) GetBundleResourceBinding(context.Context, string, string, map[string]struct{}) (bundlepkg.SkillResourceBinding, error) {
	if store.getResourceBindingErr != nil {
		return bundlepkg.SkillResourceBinding{}, store.getResourceBindingErr
	}
	return store.resourceBinding, nil
}

func (s *fakeGatewayServer) ListenAndServe() error {
	if s.listenStarted != nil {
		close(s.listenStarted)
		s.listenStarted = nil
	}
	return s.listenErr
}

func (s *fakeGatewayServer) Shutdown(context.Context) error {
	s.shutdownCalled = true
	return s.shutdownErr
}

func panicFatalf(format string, args ...any) {
	panic(fatalPanic{message: fmt.Sprintf(format, args...)})
}

func TestMainUsesInjectedRuntime(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	server := &fakeGatewayServer{listenErr: http.ErrServerClosed}

	previous := buildGatewayRuntime
	buildGatewayRuntime = func() gatewayRuntime {
		return gatewayRuntime{
			newPostgresDB: func() (*sql.DB, error) { return db, nil },
			pingPostgres:  func(*sql.DB) error { return nil },
			ensureSchema:  func(*sql.DB) error { return nil },
			newSkillStorage: func() (skills.Storage, error) {
				return stubSkillStorage{}, nil
			},
			newAggregator: func(db *sql.DB) toolAggregator { return mcp.NewSchemaAggregator(db) },
			newBundleStore: func(db *sql.DB, _ skills.Storage) bundleStoreAPI {
				return bundlepkg.NewStore(db)
			},
			newEngine: func(db *sql.DB, aggregator toolAggregator, bundleStore bundleStoreAPI, skillStorage skills.Storage) *gin.Engine {
				return gin.New()
			},
			newServer:     func(http.Handler) gatewayServer { return server },
			notifySignals: func(ch chan<- os.Signal, _ ...os.Signal) { ch <- syscall.SIGTERM },
			logFatalf:     panicFatalf,
			logPrintf:     func(string, ...any) {},
			logPrintln:    func(...any) {},
		}
	}
	defer func() {
		buildGatewayRuntime = previous
		_ = db.Close()
	}()

	main()

	if !server.shutdownCalled {
		t.Fatal("main() did not shut down the server")
	}
}

func TestRunGatewayFatalPaths(t *testing.T) {
	testCases := []struct {
		name    string
		runtime gatewayRuntime
		want    string
	}{
		{
			name: "database open failure",
			runtime: gatewayRuntime{
				newPostgresDB: func() (*sql.DB, error) { return nil, errors.New("open boom") },
				logFatalf:     panicFatalf,
			},
			want: "初始化 PostgreSQL 连接失败: open boom",
		},
		{
			name: "database ping failure",
			runtime: func() gatewayRuntime {
				db, _, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New() error = %v", err)
				}
				return gatewayRuntime{
					newPostgresDB: func() (*sql.DB, error) { return db, nil },
					pingPostgres:  func(*sql.DB) error { return errors.New("ping boom") },
					logFatalf:     panicFatalf,
					logPrintf:     func(string, ...any) {},
				}
			}(),
			want: "连接 PostgreSQL 失败: ping boom",
		},
		{
			name: "schema init failure",
			runtime: func() gatewayRuntime {
				db, _, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New() error = %v", err)
				}
				return gatewayRuntime{
					newPostgresDB: func() (*sql.DB, error) { return db, nil },
					pingPostgres:  func(*sql.DB) error { return nil },
					ensureSchema:  func(*sql.DB) error { return errors.New("schema boom") },
					logFatalf:     panicFatalf,
					logPrintf:     func(string, ...any) {},
				}
			}(),
			want: "初始化 PostgreSQL 表结构失败: schema boom",
		},
		{
			name: "skill storage init failure",
			runtime: func() gatewayRuntime {
				db, _, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New() error = %v", err)
				}
				return gatewayRuntime{
					newPostgresDB: func() (*sql.DB, error) { return db, nil },
					pingPostgres:  func(*sql.DB) error { return nil },
					ensureSchema:  func(*sql.DB) error { return nil },
					newSkillStorage: func() (skills.Storage, error) {
						return nil, errors.New("storage boom")
					},
					logFatalf:  panicFatalf,
					logPrintf:  func(string, ...any) {},
					logPrintln: func(...any) {},
				}
			}(),
			want: "初始化 skill 存储服务失败: storage boom",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			defer func() {
				recovered := recover()
				panicValue, ok := recovered.(fatalPanic)
				if !ok || panicValue.message != testCase.want {
					t.Fatalf("panic = %#v, want %q", recovered, testCase.want)
				}
			}()

			runGateway(testCase.runtime)
		})
	}
}

func TestRunGatewayHandlesShutdownFailure(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	server := &fakeGatewayServer{listenErr: http.ErrServerClosed, shutdownErr: errors.New("shutdown boom")}

	defer func() {
		recovered := recover()
		panicValue, ok := recovered.(fatalPanic)
		if !ok || panicValue.message != "优雅停机失败: shutdown boom" {
			t.Fatalf("panic = %#v", recovered)
		}
		_ = db.Close()
	}()

	runGateway(gatewayRuntime{
		newPostgresDB: func() (*sql.DB, error) { return db, nil },
		pingPostgres:  func(*sql.DB) error { return nil },
		ensureSchema:  func(*sql.DB) error { return nil },
		newSkillStorage: func() (skills.Storage, error) {
			return stubSkillStorage{}, nil
		},
		newAggregator: func(db *sql.DB) toolAggregator { return mcp.NewSchemaAggregator(db) },
		newBundleStore: func(db *sql.DB, _ skills.Storage) bundleStoreAPI {
			return bundlepkg.NewStore(db)
		},
		newEngine: func(db *sql.DB, aggregator toolAggregator, bundleStore bundleStoreAPI, skillStorage skills.Storage) *gin.Engine {
			return gin.New()
		},
		newServer:     func(http.Handler) gatewayServer { return server },
		notifySignals: func(ch chan<- os.Signal, _ ...os.Signal) { ch <- syscall.SIGTERM },
		logFatalf:     panicFatalf,
		logPrintf:     func(string, ...any) {},
		logPrintln:    func(...any) {},
	})
}

func TestRunGatewayLogsListenFailure(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	server := &fakeGatewayServer{listenErr: errors.New("listen boom"), listenStarted: make(chan struct{})}
	fatalMessages := make(chan string, 1)

	runGateway(gatewayRuntime{
		newPostgresDB: func() (*sql.DB, error) { return db, nil },
		pingPostgres:  func(*sql.DB) error { return nil },
		ensureSchema:  func(*sql.DB) error { return nil },
		newSkillStorage: func() (skills.Storage, error) {
			return stubSkillStorage{}, nil
		},
		newAggregator: func(db *sql.DB) toolAggregator { return mcp.NewSchemaAggregator(db) },
		newBundleStore: func(db *sql.DB, _ skills.Storage) bundleStoreAPI {
			return bundlepkg.NewStore(db)
		},
		newEngine: func(db *sql.DB, aggregator toolAggregator, bundleStore bundleStoreAPI, skillStorage skills.Storage) *gin.Engine {
			return gin.New()
		},
		newServer: func(http.Handler) gatewayServer { return server },
		notifySignals: func(ch chan<- os.Signal, _ ...os.Signal) {
			<-server.listenStarted
			ch <- syscall.SIGTERM
		},
		logFatalf: func(format string, args ...any) {
			fatalMessages <- fmt.Sprintf(format, args...)
		},
		logPrintf:  func(string, ...any) {},
		logPrintln: func(...any) {},
	})

	select {
	case message := <-fatalMessages:
		if message != "启动 HTTP 服务失败: listen boom" {
			t.Fatalf("listen failure message = %q", message)
		}
	default:
		t.Fatal("expected listen failure to be reported")
	}
	if !server.shutdownCalled {
		t.Fatal("runGateway() did not shut down the server")
	}
	_ = db.Close()
}

func TestDefaultGatewayRuntimeProvidesDependencies(t *testing.T) {
	t.Setenv("SKILL_STORAGE_ROOT", t.TempDir())
	runtime := defaultGatewayRuntime()
	if runtime.newPostgresDB == nil || runtime.pingPostgres == nil || runtime.ensureSchema == nil ||
		runtime.newSkillStorage == nil || runtime.newAggregator == nil || runtime.newBundleStore == nil ||
		runtime.newEngine == nil || runtime.newServer == nil || runtime.notifySignals == nil ||
		runtime.logFatalf == nil || runtime.logPrintf == nil || runtime.logPrintln == nil {
		t.Fatal("defaultGatewayRuntime() returned nil dependency")
	}

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	skillStorage, err := runtime.newSkillStorage()
	if err != nil || skillStorage == nil {
		t.Fatalf("newSkillStorage() = (%v, %v)", skillStorage, err)
	}
	aggregator := runtime.newAggregator(db)
	store := runtime.newBundleStore(db, skillStorage)
	engine := runtime.newEngine(db, aggregator, store, skillStorage)
	server, ok := runtime.newServer(engine).(*http.Server)
	if aggregator == nil || store == nil || engine == nil || !ok || server.Addr != serverAddr {
		t.Fatal("defaultGatewayRuntime() helper returned unexpected values")
	}
}

func TestResolveRequestedBundleNameAndPathMiddleware(t *testing.T) {
	if got := resolveRequestedBundleName(nil, " wx "); got != "wx" {
		t.Fatalf("resolveRequestedBundleName(nil) = %q", got)
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/wx/tools", nil)
	context.Set(auth.RequestedBundleContextKey, "weather")
	if got := resolveRequestedBundleName(context, "fallback"); got != "weather" {
		t.Fatalf("resolveRequestedBundleName(context) = %q", got)
	}

	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "bundleName", Value: " "}}
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	resolveBundleFromPath()(context)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("resolveBundleFromPath() status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestNewEngineBundleRoutesHandleErrors(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, &stubAggregator{}, &stubBundleStore{listActiveBundlesErr: errors.New("boom")}, stubSkillStorage{})
	request := httptest.NewRequest(http.MethodGet, "/v1/mcp/bundles", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("list bundles status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	router = newEngine(db, &stubAggregator{}, &stubBundleStore{getBundleToolsErr: bundlepkg.ErrBundleNotFound}, stubSkillStorage{})
	request = httptest.NewRequest(http.MethodGet, "/v1/mcp/bundles/wx/skills", nil)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("bundle skills status = %d, want %d", recorder.Code, http.StatusNotFound)
	}

	router = newEngine(db, &stubAggregator{}, &stubBundleStore{getBundleToolsErr: errors.New("boom")}, stubSkillStorage{})
	request = httptest.NewRequest(http.MethodGet, "/v1/mcp/bundles/wx/skills", nil)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("bundle skills internal status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestNewEngineBundleSkillsSuccess(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	router := newEngine(db, &stubAggregator{}, &stubBundleStore{
		getBundleToolsResp: bundlepkg.BundleToolsResponse{
			Bundle: bundlepkg.Bundle{BundleName: "weather"},
			Tools:  []mcp.MCPTool{{Name: "weather.current"}},
		},
	}, stubSkillStorage{})
	request := httptest.NewRequest(http.MethodGet, "/v1/mcp/bundles/wx/skills", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "weather.current") {
		t.Fatalf("bundle skills status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleToolsListUsesFallbackBundleAndPermissionFailure(t *testing.T) {
	aggregator := &stubAggregator{tools: []mcp.MCPTool{{Name: "weather.current"}}}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/tools", nil)

	handleToolsList(nil, aggregator, "wx")(context)
	if recorder.Code != http.StatusOK || aggregator.lastOptions.BundleName != "wx" {
		t.Fatalf("handleToolsList() code=%d bundle=%q", recorder.Code, aggregator.lastOptions.BundleName)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	mock.ExpectClose()
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/tools", nil)
	context.Request.Header.Set(auth.PaymentProofHeader, "proof")
	handleToolsList(db, aggregator, "wx")(context)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("handleToolsList() status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/tools", nil)
	handleToolsList(nil, &stubAggregator{err: errors.New("boom")}, "wx")(context)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("handleToolsList() aggregator status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestHandleUpsertBundleMappings(t *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid subdomain", err: bundlepkg.ErrInvalidSubdomain, wantStatus: http.StatusBadRequest, wantCode: "invalid_subdomain"},
		{name: "subdomain taken", err: bundlepkg.ErrSubdomainTaken, wantStatus: http.StatusConflict, wantCode: "subdomain_taken"},
		{name: "subdomain cooldown", err: bundlepkg.ErrSubdomainCooldown, wantStatus: http.StatusTooManyRequests, wantCode: "subdomain_cooldown"},
		{name: "subdomain cap", err: bundlepkg.ErrSubdomainChangeCap, wantStatus: http.StatusTooManyRequests, wantCode: "subdomain_change_limit_reached"},
		{name: "unknown skill", err: bundlepkg.ErrUnknownSkill, wantStatus: http.StatusBadRequest, wantCode: "unknown_skill"},
		{name: "invalid payload", err: bundlepkg.ErrInvalidSkillPayload, wantStatus: http.StatusBadRequest, wantCode: "invalid_skill_payload"},
		{name: "sync failed", err: bundlepkg.ErrSkillSyncFailed, wantStatus: http.StatusBadGateway, wantCode: "sync_skill_failed"},
		{name: "skill bundled", err: bundlepkg.ErrSkillAlreadyBundled, wantStatus: http.StatusConflict, wantCode: "skill_already_bundled"},
		{name: "generic", err: errors.New("boom"), wantStatus: http.StatusInternalServerError, wantCode: "save_bundle_failed"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &stubBundleStore{upsertBundleErr: testCase.err}
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/mcp/bundles", bytes.NewBufferString(`{"bundleName":"weather","displayName":"Weather"}`))
			context.Request.Header.Set("Content-Type", "application/json")

			handleUpsertBundle(store, true)(context)
			if recorder.Code != testCase.wantStatus || !bytes.Contains(recorder.Body.Bytes(), []byte(testCase.wantCode)) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	store := &stubBundleStore{upsertBundleResp: bundlepkg.Bundle{BundleName: "weather"}}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "bundleName", Value: "weather-path"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/v1/mcp/bundles/weather-path", bytes.NewBufferString(`{"bundleName":"ignored","displayName":"Weather","isActive":false}`))
	context.Request.Header.Set("Content-Type", "application/json")

	handleUpsertBundle(store, false)(context)
	if recorder.Code != http.StatusOK || store.lastUpsert.BundleName != "weather-path" || store.lastUpsert.IsActive {
		t.Fatalf("update status=%d upsert=%#v", recorder.Code, store.lastUpsert)
	}

	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/mcp/bundles", bytes.NewBufferString(`{`))
	context.Request.Header.Set("Content-Type", "application/json")
	handleUpsertBundle(store, true)(context)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid json status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestHandleDeactivateBundleMappings(t *testing.T) {
	testCases := []struct {
		err        error
		wantStatus int
	}{
		{err: nil, wantStatus: http.StatusOK},
		{err: bundlepkg.ErrBundleNotFound, wantStatus: http.StatusNotFound},
		{err: errors.New("boom"), wantStatus: http.StatusInternalServerError},
	}

	for _, testCase := range testCases {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Params = gin.Params{{Key: "bundleName", Value: "weather"}}
		context.Request = httptest.NewRequest(http.MethodDelete, "/v1/mcp/bundles/weather", nil)
		handleDeactivateBundle(&stubBundleStore{deactivateErr: testCase.err})(context)
		if recorder.Code != testCase.wantStatus {
			t.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
		}
	}
}

func TestNewPostgresDBSuccess(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db?sslmode=disable")
	db, err := newPostgresDB()
	if err != nil || db == nil {
		t.Fatalf("newPostgresDB() = (%v, %v)", db, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNewPostgresDBOpenFailure(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")

	previousOpenPostgres := openPostgres
	openPostgres = func(driverName string, dataSourceName string) (*sql.DB, error) {
		if driverName != "postgres" || dataSourceName != "postgres://example" {
			t.Fatalf("openPostgres() args = %q, %q", driverName, dataSourceName)
		}
		return nil, errors.New("boom")
	}
	t.Cleanup(func() {
		openPostgres = previousOpenPostgres
	})

	if _, err := newPostgresDB(); err == nil || !strings.Contains(err.Error(), "open postgres: boom") {
		t.Fatalf("newPostgresDB() error = %v", err)
	}
}

func TestEnsureSchemaExecutesAllStatements(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	for _, statement := range gatewaySchemaStatements {
		mock.ExpectExec(regexp.QuoteMeta(statement)).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	if err := ensureSchema(db); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestEnsureSchemaReturnsExecError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta(gatewaySchemaStatements[0])).
		WillReturnError(errors.New("exec boom"))

	if err := ensureSchema(db); err == nil || !strings.Contains(err.Error(), "exec schema statement: exec boom") {
		t.Fatalf("ensureSchema() error = %v", err)
	}
}

func TestHandleBundleMCPBranches(t *testing.T) {
	testCases := []struct {
		name       string
		handler    gin.HandlerFunc
		body       string
		wantStatus int
	}{
		{
			name:       "missing aggregator",
			handler:    handleBundleMCP(nil, nil, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "missing store",
			handler:    handleBundleMCP(nil, &stubAggregator{}, nil, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "invalid json",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "resources list without storage",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, nil, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "resources list bundle missing",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{listResourceBindingsErr: bundlepkg.ErrBundleNotFound}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "resources read invalid params",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":"bad"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "resources read invalid uri",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"bad"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "resources read binding missing",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{getResourceBindingErr: bundlepkg.ErrBundleSkillNotFound}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "resources read invalid path",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{resourceBinding: bundlepkg.SkillResourceBinding{ToolName: "current", SkillDirName: "weather-current"}}, errorSkillStorage{readErr: skills.ErrPathEscape}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "resources list storage error",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{resourceBindings: []bundlepkg.SkillResourceBinding{{ToolName: "current", SkillDirName: "weather-current"}}}, errorSkillStorage{listErr: errors.New("list boom")}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "tools list aggregator error",
			handler:    handleBundleMCP(nil, &stubAggregator{err: errors.New("boom")}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(testCase.body))
			context.Request.Header.Set("Content-Type", "application/json")

			testCase.handler(context)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestHandleBundleMCPSuccessResponses(t *testing.T) {
	aggregator := &stubAggregator{tools: []mcp.MCPTool{{Name: "weather.current", Description: "weather"}}}
	store := &stubBundleStore{
		resourceBindings: []bundlepkg.SkillResourceBinding{
			{ToolName: "zeta", SkillDirName: "weather-zeta"},
			{ToolName: "alpha", SkillDirName: "weather-alpha"},
		},
		resourceBinding: bundlepkg.SkillResourceBinding{ToolName: "current", SkillDirName: "weather-current"},
	}
	handler := handleBundleMCP(nil, aggregator, store, stubSkillStorage{}, "wx")

	testCases := []struct {
		name          string
		body          string
		wantStatus    int
		wantBody      string
		wantBodyEmpty bool
	}{
		{
			name:       "initialize success",
			body:       `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
			wantStatus: http.StatusOK,
			wantBody:   `"protocolVersion":"2024-11-05"`,
		},
		{
			name:          "initialized notification",
			body:          `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			wantStatus:    http.StatusOK,
			wantBodyEmpty: true,
		},
		{
			name:       "resources list success",
			body:       `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
			wantStatus: http.StatusOK,
			wantBody:   `"uri":"skillfun://skills/alpha/files/prompt.md"`,
		},
		{
			name:       "resources read success",
			body:       `{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`,
			wantStatus: http.StatusOK,
			wantBody:   `"text":"# current"`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(testCase.body))
			context.Request.Header.Set("Content-Type", "application/json")

			handler(context)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if testCase.wantBodyEmpty {
				if recorder.Body.Len() != 0 {
					t.Fatalf("body = %q, want empty", recorder.Body.String())
				}
				return
			}
			if !strings.Contains(recorder.Body.String(), testCase.wantBody) {
				t.Fatalf("body = %s, want substring %q", recorder.Body.String(), testCase.wantBody)
			}
			if testCase.name == "resources list success" && strings.Index(recorder.Body.String(), "skillfun://skills/alpha/files/prompt.md") >= strings.Index(recorder.Body.String(), "skillfun://skills/zeta/files/prompt.md") {
				t.Fatalf("resources not sorted: %s", recorder.Body.String())
			}
		})
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"cursorContext":"Shanghai","limit":2}}`))
	context.Request.Header.Set("Content-Type", "application/json")
	handler(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if aggregator.lastOptions.CursorContext != "Shanghai" || aggregator.lastOptions.Limit != 2 || aggregator.lastOptions.BundleName != "wx" {
		t.Fatalf("tools/list options = %#v", aggregator.lastOptions)
	}
}

func TestHandleBundleMCPAdditionalBranches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	for range [3]struct{}{} {
		mock.ExpectQuery(`(?s)WITH active_grants AS .*FROM payment_proofs.*`).
			WithArgs("proof").
			WillReturnError(errors.New("lookup boom"))
	}

	testCases := []struct {
		name       string
		handler    gin.HandlerFunc
		body       string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "invalid jsonrpc version",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"1.0","id":1,"method":"initialize"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "tools list invalid params",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":"bad"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "resources read without storage",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, nil, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "resources read resource missing",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{resourceBinding: bundlepkg.SkillResourceBinding{ToolName: "current", SkillDirName: "weather-current"}}, errorSkillStorage{readErr: skills.ErrResourceNotFound}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "resources read generic error",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{resourceBinding: bundlepkg.SkillResourceBinding{ToolName: "current", SkillDirName: "weather-current"}}, errorSkillStorage{readErr: errors.New("read boom")}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "tools list authorized lookup error",
			handler:    handleBundleMCP(db, &stubAggregator{tools: []mcp.MCPTool{{Name: "weather.current"}}}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			headers:    map[string]string{auth.PaymentProofHeader: "proof"},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "resources list authorized lookup error",
			handler:    handleBundleMCP(db, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
			headers:    map[string]string{auth.PaymentProofHeader: "proof"},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "resources read authorized lookup error",
			handler:    handleBundleMCP(db, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skillfun://skills/current/files/prompt.md"}}`,
			headers:    map[string]string{auth.PaymentProofHeader: "proof"},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "unknown method",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","id":1,"method":"missing"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unknown notification",
			handler:    handleBundleMCP(nil, &stubAggregator{}, &stubBundleStore{}, stubSkillStorage{}, "wx"),
			body:       `{"jsonrpc":"2.0","method":"notifications/missing"}`,
			wantStatus: http.StatusOK,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/wx/mcp", bytes.NewBufferString(testCase.body))
			context.Request.Header.Set("Content-Type", "application/json")
			for key, value := range testCase.headers {
				context.Request.Header.Set(key, value)
			}

			testCase.handler(context)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
		})
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

type stubSkillStorage struct{}

func (stubSkillStorage) Sync(_ context.Context, _ string, _ string) error {
	return nil
}

func (stubSkillStorage) ListResources(skillName string, _ string) ([]mcp.MCPResource, error) {
	return []mcp.MCPResource{
		{
			URI:      skills.BuildResourceURI(skillName, "prompt.md"),
			Name:     skillName + "/prompt.md",
			Title:    skillName + ": prompt.md",
			MimeType: "text/markdown",
		},
	}, nil
}

func (stubSkillStorage) ReadResource(skillName string, _ string, resourceURI string) (mcp.MCPResourceContent, error) {
	return mcp.MCPResourceContent{
		URI:      resourceURI,
		MimeType: "text/markdown",
		Text:     "# " + skillName,
	}, nil
}

type errorSkillStorage struct {
	stubSkillStorage
	listErr error
	readErr error
}

func (storage errorSkillStorage) ListResources(skillName string, skillDirName string) ([]mcp.MCPResource, error) {
	if storage.listErr != nil {
		return nil, storage.listErr
	}
	return storage.stubSkillStorage.ListResources(skillName, skillDirName)
}

func (storage errorSkillStorage) ReadResource(skillName string, skillDirName string, resourceURI string) (mcp.MCPResourceContent, error) {
	if storage.readErr != nil {
		return mcp.MCPResourceContent{}, storage.readErr
	}
	return storage.stubSkillStorage.ReadResource(skillName, skillDirName, resourceURI)
}
