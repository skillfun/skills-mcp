package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

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
