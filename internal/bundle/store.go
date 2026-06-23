package bundle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"skillfun-mcp/internal/mcp"
	"skillfun-mcp/internal/skills"
)

var (
	ErrBundleNotFound      = errors.New("bundle not found")
	ErrUnknownSkill        = errors.New("unknown skill")
	ErrBundleSkillNotFound = errors.New("bundle skill not found")
	ErrSkillAlreadyBundled = errors.New("skill already bundled")
	ErrInvalidSkillPayload = errors.New("invalid skill payload")
	ErrInvalidSubdomain    = errors.New("invalid subdomain")
	ErrSubdomainTaken      = errors.New("subdomain already taken")
	ErrSubdomainCooldown   = errors.New("subdomain change cooling down")
	ErrSubdomainChangeCap  = errors.New("subdomain monthly change limit reached")
	ErrSkillSyncFailed     = errors.New("skill sync failed")
)

const (
	minSubdomainLength       = 8
	maxSubdomainLength       = 16
	autoSubdomainLength      = 10
	maxSubdomainChangesMonth = 3
)

var (
	subdomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{6,14}[a-z0-9])$`)
	skillDirPattern  = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

const listActiveBundlesQuery = `
SELECT b.bundle_name, b.subdomain, b.display_name, COALESCE(b.description, ''), b.is_active, COUNT(s.tool_name) AS skill_count
FROM bundles b
LEFT JOIN bundle_skills bs ON bs.bundle_name = b.bundle_name
LEFT JOIN skills s
  ON s.tool_name = bs.tool_name
 AND s.is_active = TRUE
 AND s.sync_status = 'ready'
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
 AND s.sync_status = 'ready'
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
INSERT INTO skills (nft_id, tool_name, upstream_url, schema_json, github_url, skill_dir_name, sync_status, sync_error, is_active)
VALUES ($1, $2, NULL, $3::jsonb, $4, $5, 'pending', NULL, TRUE)
ON CONFLICT (nft_id) DO UPDATE
SET tool_name = EXCLUDED.tool_name,
    upstream_url = NULL,
    schema_json = EXCLUDED.schema_json,
    github_url = EXCLUDED.github_url,
    skill_dir_name = COALESCE(NULLIF(skills.skill_dir_name, ''), EXCLUDED.skill_dir_name),
    sync_status = COALESCE(NULLIF(skills.sync_status, ''), 'pending'),
    sync_error = NULL,
    is_active = TRUE,
    updated_at = CURRENT_TIMESTAMP
`

const activeSkillsByNamesQuery = `
SELECT tool_name
FROM skills
WHERE is_active = TRUE
  AND tool_name = ANY($1)
`

const existingSkillsByNFTIDsQuery = `
SELECT nft_id, tool_name, COALESCE(skill_dir_name, ''), COALESCE(sync_status, ''), schema_json, COALESCE(github_url, '')
FROM skills
WHERE nft_id = ANY($1)
`

const listSkillDirectoryNamesQuery = `
SELECT nft_id, COALESCE(skill_dir_name, '')
FROM skills
WHERE COALESCE(skill_dir_name, '') <> ''
`

const deleteBundleSkillsQuery = `
DELETE FROM bundle_skills
WHERE bundle_name = $1
`

const insertBundleSkillQuery = `
INSERT INTO bundle_skills (bundle_name, tool_name)
VALUES ($1, $2)
`

const preserveReadySkillQuery = `
INSERT INTO skills (nft_id, tool_name, upstream_url, schema_json, github_url, skill_dir_name, sync_status, sync_error, is_active)
VALUES ($1, $2, NULL, $3::jsonb, $4, $5, 'pending', NULL, TRUE)
ON CONFLICT (nft_id) DO UPDATE
SET skill_dir_name = COALESCE(NULLIF(skills.skill_dir_name, ''), EXCLUDED.skill_dir_name),
    sync_error = NULL,
    is_active = TRUE,
    updated_at = CURRENT_TIMESTAMP
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

const markSkillSyncReadyQuery = `
UPDATE skills
SET sync_status = 'ready',
    sync_error = NULL,
    last_synced_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE nft_id = $1
`

const markSkillSyncFailedQuery = `
UPDATE skills
SET sync_status = $2,
    sync_error = $3,
    updated_at = CURRENT_TIMESTAMP
WHERE nft_id = $1
`

const restoreReadySkillQuery = `
UPDATE skills
SET tool_name = $2,
    schema_json = $3::jsonb,
    github_url = $4,
    sync_status = 'ready',
    sync_error = $5,
    updated_at = CURRENT_TIMESTAMP
WHERE nft_id = $1
`

const promoteReadySkillQuery = `
UPDATE skills
SET tool_name = $2,
    schema_json = $3::jsonb,
    github_url = $4,
    sync_status = 'ready',
    sync_error = NULL,
    last_synced_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE nft_id = $1
`

const restoreBundleSkillToolNameQuery = `
UPDATE bundle_skills
SET tool_name = $3
WHERE bundle_name = $1
  AND tool_name = $2
`

const listBundleResourceBindingsQuery = `
SELECT s.tool_name, s.skill_dir_name
FROM bundle_skills bs
JOIN bundles b
  ON b.bundle_name = bs.bundle_name
 AND b.is_active = TRUE
JOIN skills s
  ON s.tool_name = bs.tool_name
 AND s.is_active = TRUE
 AND s.sync_status = 'ready'
WHERE b.subdomain = $1
  AND COALESCE(s.skill_dir_name, '') <> ''
ORDER BY s.tool_name ASC
`

// SkillSyncer 定义 GitHub skill 内容同步能力。
type SkillSyncer interface {
	Sync(ctx context.Context, githubURL string, skillDirName string) error
}

func publishedToolNames(toolNames []string, preparedSkills []preparedManagedSkill) []string {
	if len(preparedSkills) == 0 {
		return toolNames
	}

	seen := make(map[string]struct{}, len(preparedSkills))
	published := make([]string, 0, len(preparedSkills))
	for _, skill := range preparedSkills {
		toolName := skill.Name
		if skill.DeferMetadataSync && skill.PreviousToolName != "" {
			toolName = skill.PreviousToolName
		}
		if _, exists := seen[toolName]; exists {
			continue
		}
		seen[toolName] = struct{}{}
		published = append(published, toolName)
	}

	return published
}

func (s *Store) lockManagedSkills(managedSkills []ManagedSkillInput) func() {
	if len(managedSkills) == 0 {
		return func() {}
	}

	nftIDs := make([]int64, 0, len(managedSkills))
	seen := make(map[int64]struct{}, len(managedSkills))
	for _, skill := range managedSkills {
		if skill.NFTID <= 0 {
			continue
		}
		if _, exists := seen[skill.NFTID]; exists {
			continue
		}
		seen[skill.NFTID] = struct{}{}
		nftIDs = append(nftIDs, skill.NFTID)
	}
	sort.Slice(nftIDs, func(i, j int) bool {
		return nftIDs[i] < nftIDs[j]
	})

	locks := make([]*sync.Mutex, 0, len(nftIDs))
	s.locksMu.Lock()
	for _, nftID := range nftIDs {
		lock, exists := s.skillLocks[nftID]
		if !exists {
			lock = &sync.Mutex{}
			s.skillLocks[nftID] = lock
		}
		locks = append(locks, lock)
	}
	s.locksMu.Unlock()

	for _, lock := range locks {
		lock.Lock()
	}

	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

type StoreOption func(*Store)

// Store 封装 bundle 的读写逻辑。
type Store struct {
	db          *sql.DB
	skillSyncer SkillSyncer
	locksMu     sync.Mutex
	skillLocks  map[int64]*sync.Mutex
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
	GitHubURL   string
}

// SkillResourceBinding 表示 bundle 中一个可读取资源的 skill 绑定。
type SkillResourceBinding struct {
	ToolName     string
	SkillDirName string
}

type preparedManagedSkill struct {
	ManagedSkillInput
	BundleName         string
	SkillDirName       string
	SchemaJSON         []byte
	PreviousToolName   string
	PreviousGitHubURL  string
	PreviousSchemaJSON []byte
	PreviousSyncStatus string
	DeferMetadataSync  bool
}

type existingSkillRecord struct {
	NFTID        int64
	ToolName     string
	SkillDirName string
	SyncStatus   string
	SchemaJSON   []byte
	PreviousURL  string
}

// WithSkillSyncer 为 bundle store 注入 skill 同步器。
func WithSkillSyncer(skillSyncer SkillSyncer) StoreOption {
	return func(store *Store) {
		store.skillSyncer = skillSyncer
	}
}

// NewStore 创建 bundle store。
func NewStore(db *sql.DB, options ...StoreOption) *Store {
	store := &Store{
		db:         db,
		skillLocks: make(map[int64]*sync.Mutex),
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store
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

// ListBundleResourceBindings 返回当前 bundle 下所有可暴露资源的 skill。
func (s *Store) ListBundleResourceBindings(ctx context.Context, bundleName string, allowedToolNames map[string]struct{}) ([]SkillResourceBinding, error) {
	bundleName = strings.TrimSpace(bundleName)
	if _, err := s.getActiveBundle(ctx, bundleName); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, listBundleResourceBindingsQuery, bundleName)
	if err != nil {
		return nil, fmt.Errorf("list bundle resource bindings: %w", err)
	}
	defer rows.Close()

	var bindings []SkillResourceBinding
	for rows.Next() {
		var binding SkillResourceBinding
		if err := rows.Scan(&binding.ToolName, &binding.SkillDirName); err != nil {
			return nil, fmt.Errorf("scan bundle resource binding: %w", err)
		}
		if !isToolAllowed(binding.ToolName, allowedToolNames) {
			continue
		}
		bindings = append(bindings, binding)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bundle resource bindings: %w", err)
	}

	return bindings, nil
}

// GetBundleResourceBinding 校验并返回指定 bundle skill 的资源绑定。
func (s *Store) GetBundleResourceBinding(ctx context.Context, bundleName string, toolName string, allowedToolNames map[string]struct{}) (SkillResourceBinding, error) {
	bindings, err := s.ListBundleResourceBindings(ctx, bundleName, allowedToolNames)
	if err != nil {
		return SkillResourceBinding{}, err
	}

	for _, binding := range bindings {
		if binding.ToolName == strings.TrimSpace(toolName) {
			return binding, nil
		}
	}

	return SkillResourceBinding{}, ErrBundleSkillNotFound
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
	if len(managedSkills) > 0 && s.skillSyncer == nil {
		return Bundle{}, fmt.Errorf("skill syncer is nil")
	}
	unlockSkills := s.lockManagedSkills(managedSkills)
	defer unlockSkills()

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

	preparedManagedSkills, err := prepareManagedSkills(ctx, tx, input.BundleName, managedSkills)
	if err != nil {
		return Bundle{}, err
	}

	if err := upsertManagedSkills(ctx, tx, preparedManagedSkills); err != nil {
		return Bundle{}, err
	}

	if err := replaceBundleSkills(ctx, tx, input.BundleName, publishedToolNames(toolNames, preparedManagedSkills)); err != nil {
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

	if err := s.syncPreparedSkills(ctx, preparedManagedSkills); err != nil {
		return Bundle{}, err
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

func (s *Store) promoteReadySkill(ctx context.Context, skill preparedManagedSkill) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin promote skill transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		promoteReadySkillQuery,
		skill.NFTID,
		skill.Name,
		skill.SchemaJSON,
		nullableString(skill.GitHubURL),
	); err != nil {
		return fmt.Errorf("promote ready skill metadata: %w", err)
	}

	if skill.PreviousToolName != "" && skill.PreviousToolName != skill.Name {
		if _, err := tx.ExecContext(ctx, restoreBundleSkillToolNameQuery, skill.BundleName, skill.PreviousToolName, skill.Name); err != nil {
			return fmt.Errorf("promote bundle skill tool name: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit promote skill transaction: %w", err)
	}

	return nil
}

func (s *Store) syncPreparedSkills(ctx context.Context, preparedSkills []preparedManagedSkill) error {
	var failedSkills []string
	for _, skill := range preparedSkills {
		if err := s.skillSyncer.Sync(ctx, skill.GitHubURL, skill.SkillDirName); err != nil {
			if skill.PreviousSyncStatus == "ready" {
				if restoreErr := s.restoreReadySkill(ctx, skill, err.Error()); restoreErr != nil {
					return fmt.Errorf("restore ready skill %q after failed sync: %w", skill.Name, restoreErr)
				}
			} else if markErr := s.markSkillSyncFailed(ctx, skill.NFTID, "sync_failed", err.Error()); markErr != nil {
				return fmt.Errorf("mark skill sync failed for %q: %w", skill.Name, markErr)
			}
			failedSkills = append(failedSkills, skill.Name)
			continue
		}

		if skill.DeferMetadataSync {
			if err := s.promoteReadySkill(ctx, skill); err != nil {
				return fmt.Errorf("promote ready skill %q: %w", skill.Name, err)
			}
		} else if err := s.markSkillSyncReady(ctx, skill.NFTID); err != nil {
			return fmt.Errorf("mark skill sync ready for %q: %w", skill.Name, err)
		}
	}

	if len(failedSkills) > 0 {
		return fmt.Errorf("%w: %s", ErrSkillSyncFailed, strings.Join(failedSkills, ", "))
	}

	return nil
}

func (s *Store) markSkillSyncReady(ctx context.Context, nftID int64) error {
	if _, err := s.db.ExecContext(ctx, markSkillSyncReadyQuery, nftID); err != nil {
		return fmt.Errorf("mark skill ready: %w", err)
	}

	return nil
}

func (s *Store) markSkillSyncFailed(ctx context.Context, nftID int64, syncStatus string, syncError string) error {
	if _, err := s.db.ExecContext(ctx, markSkillSyncFailedQuery, nftID, syncStatus, syncError); err != nil {
		return fmt.Errorf("mark skill failed: %w", err)
	}

	return nil
}

func (s *Store) restoreReadySkill(ctx context.Context, skill preparedManagedSkill, syncError string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin restore skill transaction: %w", err)
	}
	defer tx.Rollback()

	if skill.PreviousToolName != "" && skill.PreviousToolName != skill.Name {
		if _, err := tx.ExecContext(ctx, restoreBundleSkillToolNameQuery, skill.BundleName, skill.Name, skill.PreviousToolName); err != nil {
			return fmt.Errorf("restore bundle skill tool name: %w", err)
		}
	}

	if _, err := tx.ExecContext(
		ctx,
		restoreReadySkillQuery,
		skill.NFTID,
		skill.PreviousToolName,
		skill.PreviousSchemaJSON,
		nullableString(skill.PreviousGitHubURL),
		syncError,
	); err != nil {
		return fmt.Errorf("restore ready skill metadata: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit restore skill transaction: %w", err)
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

func upsertManagedSkills(ctx context.Context, tx *sql.Tx, skillsToUpsert []preparedManagedSkill) error {
	for _, skill := range skillsToUpsert {
		query := upsertSkillQuery
		if skill.DeferMetadataSync {
			query = preserveReadySkillQuery
		}
		if _, err := tx.ExecContext(
			ctx,
			query,
			skill.NFTID,
			skill.Name,
			skill.SchemaJSON,
			skill.GitHubURL,
			skill.SkillDirName,
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

func prepareManagedSkills(ctx context.Context, tx *sql.Tx, bundleName string, managedSkills []ManagedSkillInput) ([]preparedManagedSkill, error) {
	if len(managedSkills) == 0 {
		return nil, nil
	}

	existingSkillsByNFTID, err := loadExistingSkillsByNFTID(ctx, tx, managedSkills)
	if err != nil {
		return nil, err
	}

	occupiedDirNames, err := loadOccupiedSkillDirNames(ctx, tx)
	if err != nil {
		return nil, err
	}

	preparedSkills := make([]preparedManagedSkill, 0, len(managedSkills))
	for _, skill := range managedSkills {
		schemaJSON, err := marshalSkillSchema(skill)
		if err != nil {
			return nil, err
		}

		existingSkill := existingSkillsByNFTID[skill.NFTID]
		skillDirName := existingSkill.SkillDirName
		if skillDirName == "" {
			skillDirName = allocateSkillDirName(skill.Name, occupiedDirNames)
		}
		occupiedDirNames[skillDirName] = struct{}{}

		if _, err := skills.ParseGitHubURL(skill.GitHubURL); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSkillPayload, err)
		}

		preparedSkills = append(preparedSkills, preparedManagedSkill{
			ManagedSkillInput: ManagedSkillInput{
				NFTID:       skill.NFTID,
				Name:        skill.Name,
				Description: skill.Description,
				InputSchema: skill.InputSchema,
				GitHubURL:   skill.GitHubURL,
			},
			BundleName:         bundleName,
			SkillDirName:       skillDirName,
			SchemaJSON:         schemaJSON,
			PreviousToolName:   existingSkill.ToolName,
			PreviousGitHubURL:  existingSkill.PreviousURL,
			PreviousSchemaJSON: existingSkill.SchemaJSON,
			PreviousSyncStatus: existingSkill.SyncStatus,
			DeferMetadataSync:  existingSkill.SyncStatus == "ready",
		})
	}

	return preparedSkills, nil
}

func loadExistingSkillsByNFTID(ctx context.Context, tx *sql.Tx, managedSkills []ManagedSkillInput) (map[int64]existingSkillRecord, error) {
	nftIDs := make([]int64, 0, len(managedSkills))
	for _, skill := range managedSkills {
		nftIDs = append(nftIDs, skill.NFTID)
	}

	rows, err := tx.QueryContext(ctx, existingSkillsByNFTIDsQuery, pq.Array(nftIDs))
	if err != nil {
		return nil, fmt.Errorf("load existing skills: %w", err)
	}
	defer rows.Close()

	records := make(map[int64]existingSkillRecord, len(nftIDs))
	for rows.Next() {
		var record existingSkillRecord
		if err := rows.Scan(&record.NFTID, &record.ToolName, &record.SkillDirName, &record.SyncStatus, &record.SchemaJSON, &record.PreviousURL); err != nil {
			return nil, fmt.Errorf("scan existing skill: %w", err)
		}
		records[record.NFTID] = record
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing skills: %w", err)
	}

	return records, nil
}

func loadOccupiedSkillDirNames(ctx context.Context, tx *sql.Tx) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, listSkillDirectoryNamesQuery)
	if err != nil {
		return nil, fmt.Errorf("load skill directory names: %w", err)
	}
	defer rows.Close()

	occupied := make(map[string]struct{})
	for rows.Next() {
		var nftID int64
		var skillDirName string
		if err := rows.Scan(&nftID, &skillDirName); err != nil {
			return nil, fmt.Errorf("scan skill directory name: %w", err)
		}
		if skillDirName != "" {
			occupied[skillDirName] = struct{}{}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skill directory names: %w", err)
	}

	return occupied, nil
}

func allocateSkillDirName(skillName string, occupied map[string]struct{}) string {
	baseName := normalizeSkillDirBase(skillName)
	candidate := baseName
	index := 2
	for {
		if _, exists := occupied[candidate]; !exists {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", baseName, index)
		index++
	}
}

func normalizeSkillDirBase(skillName string) string {
	skillName = strings.TrimSpace(skillName)
	skillName = strings.ReplaceAll(skillName, " ", "_")
	skillName = skillDirPattern.ReplaceAllString(skillName, "_")
	skillName = strings.Trim(skillName, "._-")
	for strings.Contains(skillName, "__") {
		skillName = strings.ReplaceAll(skillName, "__", "_")
	}
	if skillName == "" {
		return "skill"
	}

	return skillName
}

func normalizeBundleSkillInputs(toolNames []string, managedSkills []ManagedSkillInput) ([]string, []ManagedSkillInput, error) {
	if len(managedSkills) == 0 {
		return normalizeToolNames(toolNames), nil, nil
	}

	seenToolNames := make(map[string]struct{}, len(managedSkills))
	normalizedSkills := make([]ManagedSkillInput, 0, len(managedSkills))
	normalizedToolNames := make([]string, 0, len(managedSkills))
	for _, skill := range managedSkills {
		skill.Name = strings.TrimSpace(skill.Name)
		skill.Description = strings.TrimSpace(skill.Description)
		skill.GitHubURL = strings.TrimSpace(skill.GitHubURL)
		if skill.Name == "" {
			return nil, nil, fmt.Errorf("%w: skill name is required", ErrInvalidSkillPayload)
		}
		if _, exists := seenToolNames[skill.Name]; exists {
			continue
		}
		if skill.GitHubURL == "" {
			return nil, nil, fmt.Errorf("%w: githubUrl is required for %s", ErrInvalidSkillPayload, skill.Name)
		}

		seenToolNames[skill.Name] = struct{}{}
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

func isToolAllowed(toolName string, allowedToolNames map[string]struct{}) bool {
	if allowedToolNames == nil {
		return true
	}
	_, ok := allowedToolNames[toolName]
	return ok
}

func nullableDescription(description string) interface{} {
	if description == "" {
		return nil
	}
	return description
}

func nullableString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
