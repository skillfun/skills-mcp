package mcp

import (
	"context"
	"regexp"
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

type staticSemanticFilter struct {
	matches []ToolMatch
	err     error
}

func (f staticSemanticFilter) Filter(_ context.Context, _ string, _ string, _ map[string]struct{}, _ int) ([]ToolMatch, error) {
	return f.matches, f.err
}
