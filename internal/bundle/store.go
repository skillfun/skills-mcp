package bundle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"

	"skillfun-mcp/internal/mcp"
)

var (
	ErrBundleNotFound      = errors.New("bundle not found")
	ErrUnknownSkill        = errors.New("unknown skill")
	ErrSkillAlreadyBundled = errors.New("skill already bundled")
	ErrInvalidSkillPayload = errors.New("invalid skill payload")
	ErrInvalidSubdomain    = errors.New("invalid subdomain")
	ErrSubdomainTaken      = errors.New("subdomain already taken")
	ErrSubdomainCooldown   = errors.New("subdomain change cooling down")
	ErrSubdomainChangeCap  = errors.New("subdomain monthly change limit reached")
)

const (
	minSubdomainLength       = 8
	maxSubdomainLength       = 16
	autoSubdomainLength      = 10
	maxSubdomainChangesMonth = 3
)

var subdomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{6,14}[a-z0-9])$`)

const listActiveBundlesQuery = `
SELECT b.bundle_name, b.subdomain, b.display_name, COALESCE(b.description, ''), b.is_active, COUNT(s.tool_name) AS skill_count
FROM bundles b
LEFT JOIN bundle_skills bs ON bs.bundle_name = b.bundle_name
LEFT JOIN skills s
  ON s.tool_name = bs.tool_name
 AND s.is_active = TRUE
WHERE b.is_active = TRUE
GROUP BY b.bundle_name, b.subdomain, b.display_name, b.description, b.is_active
ORDER BY b.bundle_name ASC
`

const getActiveBundleQuery = `
SELECT bundle_name, subdomain, display_name, COALESCE(description, ''), is_active
FROM bundles
WHERE subdomain = $1
  AND is_active = TRUE
LIMIT 1
`

const getBundleQuery = `
SELECT bundle_name, subdomain, display_name, COALESCE(description, ''), is_active
FROM bundles
WHERE bundle_name = $1
LIMIT 1
`

const listBundleToolsQuery = `
SELECT s.tool_name, s.schema_json
FROM bundle_skills bs
JOIN bundles b
  ON b.bundle_name = bs.bundle_name
 AND b.is_active = TRUE
JOIN skills s
  ON s.tool_name = bs.tool_name
 AND s.is_active = TRUE
WHERE b.subdomain = $1
ORDER BY s.tool_name ASC
`

const upsertBundleQuery = `
INSERT INTO bundles (bundle_name, subdomain, display_name, description, is_active)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (bundle_name) DO UPDATE
SET subdomain = EXCLUDED.subdomain,
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    is_active = EXCLUDED.is_active,
    updated_at = CURRENT_TIMESTAMP
`

const upsertSkillQuery = `
INSERT INTO skills (nft_id, tool_name, upstream_url, schema_json, is_active)
VALUES ($1, $2, NULL, $3::jsonb, TRUE)
ON CONFLICT (nft_id) DO UPDATE
SET tool_name = EXCLUDED.tool_name,
    upstream_url = NULL,
    schema_json = EXCLUDED.schema_json,
    is_active = TRUE,
    updated_at = CURRENT_TIMESTAMP
`

const activeSkillsByNamesQuery = `
SELECT tool_name
FROM skills
WHERE is_active = TRUE
  AND tool_name = ANY($1)
`

const deleteBundleSkillsQuery = `
DELETE FROM bundle_skills
WHERE bundle_name = $1
`

const insertBundleSkillQuery = `
INSERT INTO bundle_skills (bundle_name, tool_name)
VALUES ($1, $2)
`

const deactivateBundleQuery = `
UPDATE bundles
SET is_active = FALSE,
    updated_at = CURRENT_TIMESTAMP
WHERE bundle_name = $1
`

const subdomainExistsQuery = `
SELECT EXISTS (
	SELECT 1
	FROM bundles
	WHERE subdomain = $1
)
`

const monthlySubdomainChangeCountQuery = `
SELECT COUNT(*)
FROM bundle_subdomain_changes
WHERE bundle_name = $1
  AND changed_at >= date_trunc('month', CURRENT_TIMESTAMP)
`

const latestSubdomainChangeQuery = `
SELECT changed_at
FROM bundle_subdomain_changes
WHERE bundle_name = $1
ORDER BY changed_at DESC
LIMIT 1
`

const insertSubdomainChangeQuery = `
INSERT INTO bundle_subdomain_changes (bundle_name, old_subdomain, new_subdomain)
VALUES ($1, $2, $3)
`

// Store 封装 bundle 的读写逻辑。
type Store struct {
	db *sql.DB
}

// Bundle 表示对外返回的 bundle 元数据。
type Bundle struct {
	BundleName  string `json:"bundleName"`
	Subdomain   string `json:"subdomain"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	IsActive    bool   `json:"isActive"`
	SkillCount  int    `json:"skillCount,omitempty"`
}

// BundleToolsResponse 表示 bundle 详情和其下的 skills。
type BundleToolsResponse struct {
	Bundle Bundle        `json:"bundle"`
	Tools  []mcp.MCPTool `json:"tools"`
}

// UpsertBundleInput 是 bundle 管理 API 的请求体。
type UpsertBundleInput struct {
	BundleName  string
	Subdomain   string
	DisplayName string
	Description string
	ToolNames   []string
	Skills      []ManagedSkillInput
	IsActive    bool
}

// ManagedSkillInput 表示 bundle 管理 API 一次性提交的 skill 定义。
type ManagedSkillInput struct {
	NFTID       int64
	Name        string
	Description string
	InputSchema json.RawMessage
}

// NewStore 创建 bundle store。
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// ListActiveBundles 返回所有公开可见的激活 bundle。
func (s *Store) ListActiveBundles(ctx context.Context) ([]Bundle, error) {
	rows, err := s.db.QueryContext(ctx, listActiveBundlesQuery)
	if err != nil {
		return nil, fmt.Errorf("list active bundles: %w", err)
	}
	defer rows.Close()

	var bundles []Bundle
	for rows.Next() {
		var bundle Bundle
		if err := rows.Scan(
			&bundle.BundleName,
			&bundle.Subdomain,
			&bundle.DisplayName,
			&bundle.Description,
			&bundle.IsActive,
			&bundle.SkillCount,
		); err != nil {
			return nil, fmt.Errorf("scan active bundle: %w", err)
		}
		bundles = append(bundles, bundle)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active bundles: %w", err)
	}

	return bundles, nil
}

// GetBundleTools 返回公开可见的 bundle 和其下 skills。
func (s *Store) GetBundleTools(ctx context.Context, bundleName string) (BundleToolsResponse, error) {
	bundleName = strings.TrimSpace(bundleName)
	bundleMetadata, err := s.getActiveBundle(ctx, bundleName)
	if err != nil {
		return BundleToolsResponse{}, err
	}

	rows, err := s.db.QueryContext(ctx, listBundleToolsQuery, bundleName)
	if err != nil {
		return BundleToolsResponse{}, fmt.Errorf("list bundle tools: %w", err)
	}
	defer rows.Close()

	var tools []mcp.MCPTool
	for rows.Next() {
		var toolName string
		var rawSchema []byte
		if err := rows.Scan(&toolName, &rawSchema); err != nil {
			return BundleToolsResponse{}, fmt.Errorf("scan bundle tool: %w", err)
		}

		var tool mcp.MCPTool
		if err := json.Unmarshal(rawSchema, &tool); err != nil {
			return BundleToolsResponse{}, fmt.Errorf("unmarshal bundle tool schema: %w", err)
		}
		tools = append(tools, tool)
	}

	if err := rows.Err(); err != nil {
		return BundleToolsResponse{}, fmt.Errorf("iterate bundle tools: %w", err)
	}

	bundleMetadata.SkillCount = len(tools)
	return BundleToolsResponse{
		Bundle: bundleMetadata,
		Tools:  tools,
	}, nil
}

// UpsertBundle 创建或更新 bundle，并按输入替换其 skill 映射。
func (s *Store) UpsertBundle(ctx context.Context, input UpsertBundleInput) (Bundle, error) {
	if s == nil || s.db == nil {
		return Bundle{}, fmt.Errorf("bundle store is nil")
	}

	input.BundleName = strings.TrimSpace(input.BundleName)
	requestedSubdomain := normalizeSubdomain(input.Subdomain)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Description = strings.TrimSpace(input.Description)
	if input.BundleName == "" || input.DisplayName == "" {
		return Bundle{}, fmt.Errorf("bundle name and display name are required")
	}

	toolNames, managedSkills, err := normalizeBundleSkillInputs(input.ToolNames, input.Skills)
	if err != nil {
		return Bundle{}, err
	}

	existingBundle, err := s.getBundle(ctx, input.BundleName)
	if err != nil && !errors.Is(err, ErrBundleNotFound) {
		return Bundle{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bundle{}, fmt.Errorf("begin bundle transaction: %w", err)
	}
	defer tx.Rollback()

	input.Subdomain, err = resolveSubdomain(ctx, tx, existingBundle, requestedSubdomain)
	if err != nil {
		return Bundle{}, err
	}

	if _, err := tx.ExecContext(
		ctx,
		upsertBundleQuery,
		input.BundleName,
		input.Subdomain,
		input.DisplayName,
		nullableDescription(input.Description),
		input.IsActive,
	); err != nil {
		var postgresErr *pq.Error
		if errors.As(err, &postgresErr) && postgresErr.Code == "23505" {
			return Bundle{}, ErrSubdomainTaken
		}
		return Bundle{}, fmt.Errorf("upsert bundle: %w", err)
	}

	if err := upsertManagedSkills(ctx, tx, managedSkills); err != nil {
		return Bundle{}, err
	}

	if err := replaceBundleSkills(ctx, tx, input.BundleName, toolNames); err != nil {
		return Bundle{}, err
	}

	if existingBundle.BundleName != "" && existingBundle.Subdomain != "" && existingBundle.Subdomain != input.Subdomain {
		if _, err := tx.ExecContext(ctx, insertSubdomainChangeQuery, input.BundleName, existingBundle.Subdomain, input.Subdomain); err != nil {
			return Bundle{}, fmt.Errorf("record subdomain change: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Bundle{}, fmt.Errorf("commit bundle transaction: %w", err)
	}

	return s.getBundle(ctx, input.BundleName)
}

// DeactivateBundle 将 bundle 软删除。
func (s *Store) DeactivateBundle(ctx context.Context, bundleName string) error {
	result, err := s.db.ExecContext(ctx, deactivateBundleQuery, strings.TrimSpace(bundleName))
	if err != nil {
		return fmt.Errorf("deactivate bundle: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deactivate bundle result: %w", err)
	}
	if rowsAffected == 0 {
		return ErrBundleNotFound
	}

	return nil
}

func (s *Store) getActiveBundle(ctx context.Context, bundleName string) (Bundle, error) {
	return s.getBundleByQuery(ctx, getActiveBundleQuery, bundleName)
}

func (s *Store) getBundle(ctx context.Context, bundleName string) (Bundle, error) {
	return s.getBundleByQuery(ctx, getBundleQuery, bundleName)
}

func (s *Store) getBundleByQuery(ctx context.Context, query string, bundleName string) (Bundle, error) {
	var bundle Bundle
	err := s.db.QueryRowContext(ctx, query, bundleName).Scan(
		&bundle.BundleName,
		&bundle.Subdomain,
		&bundle.DisplayName,
		&bundle.Description,
		&bundle.IsActive,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Bundle{}, ErrBundleNotFound
	}
	if err != nil {
		return Bundle{}, fmt.Errorf("get active bundle: %w", err)
	}

	return bundle, nil
}

func replaceBundleSkills(ctx context.Context, tx *sql.Tx, bundleName string, toolNames []string) error {
	if len(toolNames) == 0 {
		if _, err := tx.ExecContext(ctx, deleteBundleSkillsQuery, bundleName); err != nil {
			return fmt.Errorf("clear bundle skills: %w", err)
		}
		return nil
	}

	rows, err := tx.QueryContext(ctx, activeSkillsByNamesQuery, pq.Array(toolNames))
	if err != nil {
		return fmt.Errorf("load active skills for bundle: %w", err)
	}
	defer rows.Close()

	found := make(map[string]struct{}, len(toolNames))
	for rows.Next() {
		var toolName string
		if err := rows.Scan(&toolName); err != nil {
			return fmt.Errorf("scan active bundle skill: %w", err)
		}
		found[toolName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate active bundle skills: %w", err)
	}

	for _, toolName := range toolNames {
		if _, ok := found[toolName]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownSkill, toolName)
		}
	}

	if _, err := tx.ExecContext(ctx, deleteBundleSkillsQuery, bundleName); err != nil {
		return fmt.Errorf("clear bundle skills: %w", err)
	}

	for _, toolName := range toolNames {
		if _, err := tx.ExecContext(ctx, insertBundleSkillQuery, bundleName, toolName); err != nil {
			var postgresErr *pq.Error
			if errors.As(err, &postgresErr) && postgresErr.Code == "23505" {
				return fmt.Errorf("%w: %s", ErrSkillAlreadyBundled, toolName)
			}
			return fmt.Errorf("insert bundle skill: %w", err)
		}
	}

	return nil
}

func upsertManagedSkills(ctx context.Context, tx *sql.Tx, skills []ManagedSkillInput) error {
	for _, skill := range skills {
		schemaJSON, err := marshalSkillSchema(skill)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(
			ctx,
			upsertSkillQuery,
			skill.NFTID,
			skill.Name,
			schemaJSON,
		); err != nil {
			return fmt.Errorf("upsert managed skill %q: %w", skill.Name, err)
		}
	}

	return nil
}

func marshalSkillSchema(skill ManagedSkillInput) ([]byte, error) {
	tool := mcp.MCPTool{
		Name:        strings.TrimSpace(skill.Name),
		Description: strings.TrimSpace(skill.Description),
	}
	if tool.Name == "" || tool.Description == "" || skill.NFTID <= 0 || len(skill.InputSchema) == 0 || !json.Valid(skill.InputSchema) {
		return nil, fmt.Errorf("%w: nftId/name/description/inputSchema are required", ErrInvalidSkillPayload)
	}

	if err := json.Unmarshal(skill.InputSchema, &tool.InputSchema); err != nil {
		return nil, fmt.Errorf("%w: invalid inputSchema for %s", ErrInvalidSkillPayload, skill.Name)
	}

	schemaJSON, err := json.Marshal(tool)
	if err != nil {
		return nil, fmt.Errorf("marshal managed skill schema: %w", err)
	}

	return schemaJSON, nil
}

func normalizeBundleSkillInputs(toolNames []string, skills []ManagedSkillInput) ([]string, []ManagedSkillInput, error) {
	if len(skills) == 0 {
		return normalizeToolNames(toolNames), nil, nil
	}

	seen := make(map[string]struct{}, len(skills))
	normalizedSkills := make([]ManagedSkillInput, 0, len(skills))
	normalizedToolNames := make([]string, 0, len(skills))
	for _, skill := range skills {
		skill.Name = strings.TrimSpace(skill.Name)
		skill.Description = strings.TrimSpace(skill.Description)
		if skill.Name == "" {
			return nil, nil, fmt.Errorf("%w: skill name is required", ErrInvalidSkillPayload)
		}
		if _, exists := seen[skill.Name]; exists {
			continue
		}

		seen[skill.Name] = struct{}{}
		normalizedSkills = append(normalizedSkills, skill)
		normalizedToolNames = append(normalizedToolNames, skill.Name)
	}

	return normalizedToolNames, normalizedSkills, nil
}

func validateSubdomain(subdomain string) error {
	if subdomain == "" || len(subdomain) < minSubdomainLength || len(subdomain) > maxSubdomainLength || !subdomainPattern.MatchString(subdomain) {
		return ErrInvalidSubdomain
	}
	return nil
}

func normalizeSubdomain(subdomain string) string {
	return strings.ToLower(strings.TrimSpace(subdomain))
}

func resolveSubdomain(ctx context.Context, tx *sql.Tx, existingBundle Bundle, requestedSubdomain string) (string, error) {
	if requestedSubdomain != "" {
		if err := validateSubdomain(requestedSubdomain); err != nil {
			return "", err
		}
		if existingBundle.BundleName != "" && existingBundle.Subdomain != "" && existingBundle.Subdomain != requestedSubdomain {
			if err := ensureSubdomainChangeAllowed(ctx, tx, existingBundle.BundleName); err != nil {
				return "", err
			}
		}
		return requestedSubdomain, nil
	}

	if existingBundle.BundleName != "" && existingBundle.Subdomain != "" {
		return existingBundle.Subdomain, nil
	}

	return generateUniqueSubdomain(ctx, tx)
}

func ensureSubdomainChangeAllowed(ctx context.Context, tx *sql.Tx, bundleName string) error {
	var latestChangedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, latestSubdomainChangeQuery, bundleName).Scan(&latestChangedAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load latest subdomain change: %w", err)
	}
	if latestChangedAt.Valid && latestChangedAt.Time.After(time.Now().Add(-24*time.Hour)) {
		return ErrSubdomainCooldown
	}

	var changesThisMonth int
	if err := tx.QueryRowContext(ctx, monthlySubdomainChangeCountQuery, bundleName).Scan(&changesThisMonth); err != nil {
		return fmt.Errorf("count monthly subdomain changes: %w", err)
	}
	if changesThisMonth >= maxSubdomainChangesMonth {
		return ErrSubdomainChangeCap
	}

	return nil
}

func generateUniqueSubdomain(ctx context.Context, tx *sql.Tx) (string, error) {
	for i := 0; i < 10; i++ {
		candidate, err := generateRandomSubdomain()
		if err != nil {
			return "", err
		}

		var exists bool
		if err := tx.QueryRowContext(ctx, subdomainExistsQuery, candidate).Scan(&exists); err != nil {
			return "", fmt.Errorf("check subdomain uniqueness: %w", err)
		}
		if !exists {
			return candidate, nil
		}
	}

	return "", ErrSubdomainTaken
}

func generateRandomSubdomain() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	randomBytes := make([]byte, autoSubdomainLength)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate subdomain: %w", err)
	}

	subdomain := make([]byte, autoSubdomainLength)
	for i, randomByte := range randomBytes {
		subdomain[i] = alphabet[int(randomByte)%len(alphabet)]
	}

	return string(subdomain), nil
}

func normalizeToolNames(toolNames []string) []string {
	seen := make(map[string]struct{}, len(toolNames))
	normalized := make([]string, 0, len(toolNames))
	for _, toolName := range toolNames {
		trimmed := strings.TrimSpace(toolName)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}

		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}

	return normalized
}

func nullableDescription(description string) interface{} {
	if description == "" {
		return nil
	}
	return description
}
