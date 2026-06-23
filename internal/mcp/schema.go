package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/lib/pq"
)

const (
	DefaultSemanticLimit = 10
	maxSemanticKeywords  = 6
)

const fetchToolsByNamesQuery = `
SELECT s.tool_name, s.schema_json
FROM skills s
JOIN bundle_skills bs
  ON bs.tool_name = s.tool_name
JOIN bundles b
  ON b.bundle_name = bs.bundle_name
 AND b.is_active = TRUE
WHERE s.is_active = TRUE
  AND s.sync_status = 'ready'
  AND s.tool_name = ANY($1)
`

// ListToolsOptions 表示 tools/list 的动态筛选参数。
type ListToolsOptions struct {
	CursorContext    string
	BundleName       string
	Limit            int
	AllowedToolNames map[string]struct{}
}

// ToolMatch 表示语义筛选阶段命中的工具。
type ToolMatch struct {
	ToolName string
	Score    int
}

// SemanticFilter 定义 tools/list 的第一层候选筛选能力。
// 当前默认实现使用关键词匹配，后续可以替换成向量数据库实现。
type SemanticFilter interface {
	Filter(ctx context.Context, cursorContext string, bundleName string, allowedToolNames map[string]struct{}, limit int) ([]ToolMatch, error)
}

// KeywordSemanticFilter 使用 PostgreSQL 上的关键词打分来筛选候选工具。
type KeywordSemanticFilter struct {
	db *sql.DB
}

// SchemaAggregator 负责从 PostgreSQL 聚合当前激活的 MCP Tool Schema。
type SchemaAggregator struct {
	db             *sql.DB
	semanticFilter SemanticFilter
}

// NewSchemaAggregator 创建一个新的 SchemaAggregator。
func NewSchemaAggregator(db *sql.DB) *SchemaAggregator {
	return NewSchemaAggregatorWithFilter(db, NewKeywordSemanticFilter(db))
}

// NewSchemaAggregatorWithFilter 允许调用方替换语义筛选实现。
func NewSchemaAggregatorWithFilter(db *sql.DB, semanticFilter SemanticFilter) *SchemaAggregator {
	return &SchemaAggregator{
		db:             db,
		semanticFilter: semanticFilter,
	}
}

// NewKeywordSemanticFilter 创建默认的关键词语义筛选器。
func NewKeywordSemanticFilter(db *sql.DB) *KeywordSemanticFilter {
	return &KeywordSemanticFilter{db: db}
}

// GetAggregateTools 根据上下文和权限筛选动态返回工具定义。
func (a *SchemaAggregator) GetAggregateTools(ctx context.Context, options ListToolsOptions) ([]MCPTool, error) {
	if a == nil {
		return nil, fmt.Errorf("schema aggregator is nil")
	}

	if a.db == nil {
		return nil, fmt.Errorf("postgres db is nil")
	}

	if a.semanticFilter == nil {
		return nil, fmt.Errorf("semantic filter is nil")
	}

	matches, err := a.semanticFilter.Filter(
		ctx,
		options.CursorContext,
		options.BundleName,
		options.AllowedToolNames,
		normalizeLimit(options.Limit),
	)
	if err != nil {
		return nil, fmt.Errorf("filter semantic candidates: %w", err)
	}

	return a.loadToolSchemas(ctx, matches)
}

// Filter 根据 cursor_context 和授权集合筛选候选工具。
func (f *KeywordSemanticFilter) Filter(ctx context.Context, cursorContext string, bundleName string, allowedToolNames map[string]struct{}, limit int) ([]ToolMatch, error) {
	if f == nil {
		return nil, fmt.Errorf("keyword semantic filter is nil")
	}

	if f.db == nil {
		return nil, fmt.Errorf("postgres db is nil")
	}

	if allowedToolNames != nil && len(allowedToolNames) == 0 {
		return []ToolMatch{}, nil
	}

	tokens := tokenizeCursorContext(cursorContext)
	query, args := buildKeywordFilterQuery(tokens, strings.TrimSpace(bundleName), allowedToolNames, normalizeLimit(limit))

	rows, err := f.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query semantic tool matches: %w", err)
	}
	defer rows.Close()

	var matches []ToolMatch
	for rows.Next() {
		var match ToolMatch
		if err := rows.Scan(&match.ToolName, &match.Score); err != nil {
			return nil, fmt.Errorf("scan semantic match: %w", err)
		}

		matches = append(matches, match)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate semantic matches: %w", err)
	}

	return matches, nil
}

func (a *SchemaAggregator) loadToolSchemas(ctx context.Context, matches []ToolMatch) ([]MCPTool, error) {
	if len(matches) == 0 {
		return []MCPTool{}, nil
	}

	toolNames := make([]string, 0, len(matches))
	for _, match := range matches {
		toolNames = append(toolNames, match.ToolName)
	}

	rows, err := a.db.QueryContext(ctx, fetchToolsByNamesQuery, pq.Array(toolNames))
	if err != nil {
		return nil, fmt.Errorf("query tool schemas: %w", err)
	}
	defer rows.Close()

	toolsByName := make(map[string]MCPTool, len(matches))
	for rows.Next() {
		var toolName string
		var rawSchema []byte
		if err := rows.Scan(&toolName, &rawSchema); err != nil {
			return nil, fmt.Errorf("scan tool schema: %w", err)
		}

		var tool MCPTool
		if err := json.Unmarshal(rawSchema, &tool); err != nil {
			return nil, fmt.Errorf("unmarshal tool schema: %w", err)
		}

		toolsByName[toolName] = tool
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool schemas: %w", err)
	}

	tools := make([]MCPTool, 0, len(matches))
	for _, match := range matches {
		tool, ok := toolsByName[match.ToolName]
		if !ok {
			continue
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

func buildKeywordFilterQuery(tokens []string, bundleName string, allowedToolNames map[string]struct{}, limit int) (string, []any) {
	var builder strings.Builder
	args := make([]any, 0, len(tokens)+2)
	argumentIndex := 1

	builder.WriteString("SELECT s.tool_name, ")

	var scoreParts []string
	var matchParts []string
	for _, token := range tokens {
		placeholder := fmt.Sprintf("$%d", argumentIndex)
		scoreParts = append(
			scoreParts,
			fmt.Sprintf(
				"(CASE WHEN s.tool_name ILIKE %s THEN 6 ELSE 0 END + CASE WHEN COALESCE(s.schema_json->>'description', '') ILIKE %s THEN 3 ELSE 0 END)",
				placeholder,
				placeholder,
			),
		)
		matchParts = append(
			matchParts,
			fmt.Sprintf("(s.tool_name ILIKE %s OR COALESCE(s.schema_json->>'description', '') ILIKE %s)", placeholder, placeholder),
		)
		args = append(args, "%"+token+"%")
		argumentIndex++
	}

	if len(scoreParts) == 0 {
		builder.WriteString("0 AS score ")
	} else {
		builder.WriteString(strings.Join(scoreParts, " + "))
		builder.WriteString(" AS score ")
	}

	builder.WriteString("FROM skills s JOIN bundle_skills bs ON bs.tool_name = s.tool_name JOIN bundles b ON b.bundle_name = bs.bundle_name AND b.is_active = TRUE WHERE s.is_active = TRUE AND s.sync_status = 'ready'")

	if bundleName != "" {
		builder.WriteString(fmt.Sprintf(" AND b.subdomain = $%d", argumentIndex))
		args = append(args, bundleName)
		argumentIndex++
	}

	if allowedToolNames != nil {
		builder.WriteString(fmt.Sprintf(" AND s.tool_name = ANY($%d)", argumentIndex))
		args = append(args, pq.Array(sortedToolNames(allowedToolNames)))
		argumentIndex++
	}

	if len(matchParts) > 0 {
		builder.WriteString(" AND (")
		builder.WriteString(strings.Join(matchParts, " OR "))
		builder.WriteString(")")
		builder.WriteString(" ORDER BY score DESC, s.tool_name ASC")
	} else {
		builder.WriteString(" ORDER BY s.nft_id ASC")
	}

	builder.WriteString(fmt.Sprintf(" LIMIT $%d", argumentIndex))
	args = append(args, limit)

	return builder.String(), args
}

func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultSemanticLimit
	case limit > DefaultSemanticLimit:
		return DefaultSemanticLimit
	default:
		return limit
	}
}

func tokenizeCursorContext(cursorContext string) []string {
	lowered := strings.ToLower(strings.TrimSpace(cursorContext))
	if lowered == "" {
		return nil
	}

	seen := make(map[string]struct{})
	tokens := strings.FieldsFunc(lowered, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) < 2 {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}

		seen[token] = struct{}{}
		filtered = append(filtered, token)
		if len(filtered) == maxSemanticKeywords {
			break
		}
	}

	return filtered
}

func sortedToolNames(toolNames map[string]struct{}) []string {
	names := make([]string, 0, len(toolNames))
	for toolName := range toolNames {
		names = append(names, toolName)
	}

	// PostgreSQL 的 ANY 查询不依赖顺序，但排序后便于测试和排查问题。
	slices.Sort(names)
	return names
}
