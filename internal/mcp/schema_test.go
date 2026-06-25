package mcp

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetAggregateToolsUsesSemanticMatchesAndPreservesOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(fetchToolsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name", "schema_json"}).
				AddRow(`bundle:1001`, `{"name":"bundle:1001","description":"first tool","inputSchema":{"type":"object"}}`).
				AddRow(`bundle:1002`, `{"name":"bundle:1002","description":"second tool","inputSchema":{"type":"object"}}`),
		)

	aggregator := NewSchemaAggregatorWithFilter(db, staticSemanticFilter{
		matches: []ToolMatch{
			{ToolName: "bundle:1002", Score: 9},
			{ToolName: "bundle:1001", Score: 7},
		},
	})
	tools, err := aggregator.GetAggregateTools(context.Background(), ListToolsOptions{CursorContext: "second"})
	if err != nil {
		t.Fatalf("GetAggregateTools() error = %v", err)
	}

	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}

	if tools[0].Name != "bundle:1002" || tools[1].Name != "bundle:1001" {
		t.Fatalf("unexpected tool order: %#v", tools)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetAggregateToolsSkipsMissingSchemas(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(fetchToolsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name", "schema_json"}).
				AddRow(`bundle:1002`, `{"name":"bundle:1002","description":"second tool","inputSchema":{"type":"object"}}`),
		)

	aggregator := NewSchemaAggregatorWithFilter(db, staticSemanticFilter{
		matches: []ToolMatch{
			{ToolName: "bundle:1001", Score: 10},
			{ToolName: "bundle:1002", Score: 9},
		},
	})
	tools, err := aggregator.GetAggregateTools(context.Background(), ListToolsOptions{})
	if err != nil {
		t.Fatalf("GetAggregateTools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "bundle:1002" {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestKeywordSemanticFilterBuildsRankedQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT s.tool_name, .* FROM skills s JOIN bundle_skills.* LIMIT \$3`).
		WithArgs("%weather%", "%assistant%", 3).
		WillReturnRows(
			sqlmock.NewRows([]string{"tool_name", "score"}).
				AddRow("bundle:weather", 9).
				AddRow("bundle:assistant", 6),
		)

	filter := NewKeywordSemanticFilter(db)
	matches, err := filter.Filter(context.Background(), "weather assistant", "", nil, 3)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	if len(matches) != 2 {
		t.Fatalf("len(matches) = %d, want 2", len(matches))
	}

	if matches[0].ToolName != "bundle:weather" || matches[1].ToolName != "bundle:assistant" {
		t.Fatalf("unexpected matches: %#v", matches)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestNewSchemaAggregatorUsesKeywordFilter(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	aggregator := NewSchemaAggregator(db)
	if aggregator.db != db {
		t.Fatalf("aggregator.db = %v, want %v", aggregator.db, db)
	}
	if _, ok := aggregator.semanticFilter.(*KeywordSemanticFilter); !ok {
		t.Fatalf("semanticFilter = %T, want *KeywordSemanticFilter", aggregator.semanticFilter)
	}
}

func TestGetAggregateToolsRejectsInvalidAggregator(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	filter := staticSemanticFilter{}

	testCases := []struct {
		name       string
		aggregator *SchemaAggregator
		want       string
	}{
		{name: "nil aggregator", aggregator: nil, want: "schema aggregator is nil"},
		{name: "nil db", aggregator: &SchemaAggregator{semanticFilter: filter}, want: "postgres db is nil"},
		{name: "nil filter", aggregator: &SchemaAggregator{db: db}, want: "semantic filter is nil"},
		{name: "filter error", aggregator: &SchemaAggregator{db: db, semanticFilter: staticSemanticFilter{err: errors.New("boom")}}, want: "filter semantic candidates: boom"},
	}

	for _, testCase := range testCases {
		_, err := testCase.aggregator.GetAggregateTools(context.Background(), ListToolsOptions{})
		if err == nil || err.Error() != testCase.want {
			t.Fatalf("%s: err = %v, want %q", testCase.name, err, testCase.want)
		}
	}
}

func TestGetAggregateToolsReturnsEmptyWhenNoMatches(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	aggregator := NewSchemaAggregatorWithFilter(db, staticSemanticFilter{})
	tools, err := aggregator.GetAggregateTools(context.Background(), ListToolsOptions{})
	if err != nil {
		t.Fatalf("GetAggregateTools() error = %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("len(tools) = %d, want 0", len(tools))
	}
}

func TestKeywordSemanticFilterRejectsInvalidState(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	testCases := []struct {
		name   string
		filter *KeywordSemanticFilter
		want   string
	}{
		{name: "nil filter", filter: nil, want: "keyword semantic filter is nil"},
		{name: "nil db", filter: &KeywordSemanticFilter{}, want: "postgres db is nil"},
	}

	for _, testCase := range testCases {
		_, err := testCase.filter.Filter(context.Background(), "", "", nil, 1)
		if err == nil || err.Error() != testCase.want {
			t.Fatalf("%s: err = %v, want %q", testCase.name, err, testCase.want)
		}
	}
}

func TestKeywordSemanticFilterReturnsEmptyWhenAllowedSetEmpty(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	filter := NewKeywordSemanticFilter(db)
	matches, err := filter.Filter(context.Background(), "weather", "", map[string]struct{}{}, 3)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("len(matches) = %d, want 0", len(matches))
	}
}

func TestKeywordSemanticFilterPropagatesRowIterationError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"tool_name", "score"}).AddRow("weather.current", 9).RowError(0, sql.ErrConnDone)
	mock.ExpectQuery(`(?s)SELECT s.tool_name, .* FROM skills s JOIN bundle_skills.* LIMIT \$2`).
		WithArgs("%weather%", 3).
		WillReturnRows(rows)

	filter := NewKeywordSemanticFilter(db)
	_, err = filter.Filter(context.Background(), "weather", "", nil, 3)
	if err == nil || !strings.Contains(err.Error(), "iterate semantic matches") {
		t.Fatalf("Filter() error = %v", err)
	}
}

func TestKeywordSemanticFilterPropagatesQueryAndScanErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`(?s)SELECT s.tool_name, .* FROM skills s JOIN bundle_skills.* LIMIT \$2`).
		WithArgs("%weather%", 3).
		WillReturnError(sql.ErrConnDone)

	filter := NewKeywordSemanticFilter(db)
	_, err = filter.Filter(context.Background(), "weather", "", nil, 3)
	if err == nil || !strings.Contains(err.Error(), "query semantic tool matches") {
		t.Fatalf("Filter() query error = %v", err)
	}

	mock.ExpectQuery(`(?s)SELECT s.tool_name, .* FROM skills s JOIN bundle_skills.* LIMIT \$2`).
		WithArgs("%weather%", 3).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "score"}).AddRow("weather.current", nil))
	_, err = filter.Filter(context.Background(), "weather", "", nil, 3)
	if err == nil || !strings.Contains(err.Error(), "scan semantic match") {
		t.Fatalf("Filter() scan error = %v", err)
	}
}

func TestLoadToolSchemasRejectsInvalidJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(fetchToolsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "schema_json"}).AddRow("weather.current", `{"name":`))

	aggregator := NewSchemaAggregatorWithFilter(db, staticSemanticFilter{
		matches: []ToolMatch{{ToolName: "weather.current", Score: 9}},
	})
	_, err = aggregator.GetAggregateTools(context.Background(), ListToolsOptions{})
	if err == nil || !strings.Contains(err.Error(), "unmarshal tool schema") {
		t.Fatalf("GetAggregateTools() error = %v", err)
	}
}

func TestLoadToolSchemasPropagatesQueryScanAndRowErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	aggregator := NewSchemaAggregatorWithFilter(db, staticSemanticFilter{
		matches: []ToolMatch{{ToolName: "weather.current", Score: 9}},
	})

	mock.ExpectQuery(regexp.QuoteMeta(fetchToolsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(sql.ErrConnDone)
	_, err = aggregator.GetAggregateTools(context.Background(), ListToolsOptions{})
	if err == nil || !strings.Contains(err.Error(), "query tool schemas") {
		t.Fatalf("GetAggregateTools() query error = %v", err)
	}

	mock.ExpectQuery(regexp.QuoteMeta(fetchToolsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "schema_json"}).AddRow(nil, []byte(`{"name":"weather.current","description":"desc","inputSchema":{"type":"object"}}`)))
	_, err = aggregator.GetAggregateTools(context.Background(), ListToolsOptions{})
	if err == nil || !strings.Contains(err.Error(), "scan tool schema") {
		t.Fatalf("GetAggregateTools() scan error = %v", err)
	}

	rows := sqlmock.NewRows([]string{"tool_name", "schema_json"}).
		AddRow("weather.current", []byte(`{"name":"weather.current","description":"desc","inputSchema":{"type":"object"}}`)).
		RowError(0, sql.ErrConnDone)
	mock.ExpectQuery(regexp.QuoteMeta(fetchToolsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)
	_, err = aggregator.GetAggregateTools(context.Background(), ListToolsOptions{})
	if err == nil || !strings.Contains(err.Error(), "iterate tool schemas") {
		t.Fatalf("GetAggregateTools() row error = %v", err)
	}
}

func TestBuildKeywordFilterQueryIncludesBundleAndAllowed(t *testing.T) {
	query, args := buildKeywordFilterQuery([]string{"weather", "assistant"}, "wx", map[string]struct{}{
		"weather.current":  {},
		"weather.forecast": {},
	}, 3)

	if !strings.Contains(query, "b.subdomain = $3") || !strings.Contains(query, "s.tool_name = ANY($4)") || !strings.Contains(query, "ORDER BY score DESC, s.tool_name ASC") {
		t.Fatalf("query = %q", query)
	}
	if len(args) != 5 {
		t.Fatalf("len(args) = %d, want 5", len(args))
	}
}

func TestBuildKeywordFilterQueryWithoutTokensOrdersByNFTID(t *testing.T) {
	query, args := buildKeywordFilterQuery(nil, "", nil, 2)

	if !strings.Contains(query, "0 AS score") || !strings.Contains(query, "ORDER BY s.nft_id ASC") {
		t.Fatalf("query = %q", query)
	}
	if len(args) != 1 || args[0] != 2 {
		t.Fatalf("args = %#v", args)
	}
}

func TestNormalizeLimit(t *testing.T) {
	testCases := []struct {
		limit int
		want  int
	}{
		{limit: 0, want: DefaultSemanticLimit},
		{limit: -1, want: DefaultSemanticLimit},
		{limit: DefaultSemanticLimit + 1, want: DefaultSemanticLimit},
		{limit: 3, want: 3},
	}

	for _, testCase := range testCases {
		if got := normalizeLimit(testCase.limit); got != testCase.want {
			t.Fatalf("normalizeLimit(%d) = %d, want %d", testCase.limit, got, testCase.want)
		}
	}
}

func TestTokenizeCursorContext(t *testing.T) {
	tokens := tokenizeCursorContext(" Weather, weather! a b assistant forecast current hourly radar alerts more ")
	if strings.Join(tokens, ",") != "weather,assistant,forecast,current,hourly,radar" {
		t.Fatalf("tokenizeCursorContext() = %#v", tokens)
	}
	if tokens := tokenizeCursorContext("   "); tokens != nil {
		t.Fatalf("expected nil tokens, got %#v", tokens)
	}
}

func TestSortedToolNames(t *testing.T) {
	names := sortedToolNames(map[string]struct{}{
		"weather.current":  {},
		"weather.forecast": {},
		"alerts":           {},
	})
	if strings.Join(names, ",") != "alerts,weather.current,weather.forecast" {
		t.Fatalf("sortedToolNames() = %#v", names)
	}
}

type staticSemanticFilter struct {
	matches []ToolMatch
	err     error
}

func (f staticSemanticFilter) Filter(_ context.Context, _ string, _ string, _ map[string]struct{}, _ int) ([]ToolMatch, error) {
	return f.matches, f.err
}
