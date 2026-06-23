package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"

	"skillfun-mcp/internal/auth"
	bundlepkg "skillfun-mcp/internal/bundle"
	"skillfun-mcp/internal/mcp"
	"skillfun-mcp/internal/skills"
)

const (
	serverAddr          = ":8080"
	postgresPingTimeout = 5 * time.Second
	shutdownTimeout     = 10 * time.Second
)

func main() {
	db, err := newPostgresDB()
	if err != nil {
		log.Fatalf("初始化 PostgreSQL 连接失败: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("关闭 PostgreSQL 连接失败: %v", err)
		}
	}()

	if err := pingPostgres(db); err != nil {
		log.Fatalf("连接 PostgreSQL 失败: %v", err)
	}

	if err := ensureSchema(db); err != nil {
		log.Fatalf("初始化 PostgreSQL 表结构失败: %v", err)
	}

	skillStorage, err := skills.NewServiceFromEnv()
	if err != nil {
		log.Fatalf("初始化 skill 存储服务失败: %v", err)
	}

	aggregator := mcp.NewSchemaAggregator(db)
	bundleStore := bundlepkg.NewStore(db, bundlepkg.WithSkillSyncer(skillStorage))
	engine := newEngine(db, aggregator, bundleStore, skillStorage)

	server := &http.Server{
		Addr:              serverAddr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("SkillFun MCP Gateway 已启动，监听地址: %s", serverAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("启动 HTTP 服务失败: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("收到停机信号，开始优雅停机...")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("优雅停机失败: %v", err)
	}

	log.Println("网关已安全退出")
}

func newEngine(db *sql.DB, aggregator *mcp.SchemaAggregator, bundleStore *bundlepkg.Store, skillStorage skills.Storage) *gin.Engine {
	engine := gin.New()
	engine.Use(
		gin.Logger(),
		gin.Recovery(),
		cors.New(cors.Config{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{
				http.MethodGet,
				http.MethodPost,
				http.MethodPut,
				http.MethodDelete,
				http.MethodOptions,
			},
			AllowHeaders: []string{
				"Origin",
				"Content-Type",
				"Accept",
				"Authorization",
				auth.PaymentProofHeader,
			},
			ExposeHeaders: []string{"Content-Length"},
			MaxAge:        12 * time.Hour,
		}),
	)

	engine.GET("/v1/mcp/bundles", func(c *gin.Context) {
		bundles, err := bundleStore.ListActiveBundles(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "load_bundles_failed",
				"message": "从 PostgreSQL 读取 bundle 列表失败",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"bundles": bundles})
	})

	engine.GET("/v1/mcp/bundles/:subdomain/skills", func(c *gin.Context) {
		response, err := bundleStore.GetBundleTools(c.Request.Context(), c.Param("subdomain"))
		if err != nil {
			if errors.Is(err, bundlepkg.ErrBundleNotFound) {
				c.JSON(http.StatusNotFound, gin.H{
					"error":   "bundle_not_found",
					"message": "未找到指定 bundle",
				})
				return
			}

			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "load_bundle_skills_failed",
				"message": "读取 bundle 下的 skills 失败",
			})
			return
		}

		c.JSON(http.StatusOK, response)
	})

	hostBundleRoutes := engine.Group("/", resolveBundleFromHost())
	hostBundleRoutes.GET("/tools", handleToolsList(db, aggregator, ""))
	hostBundleRoutes.POST("/mcp", handleBundleMCP(db, aggregator, bundleStore, skillStorage, ""))

	pathBundleRoutes := engine.Group("/:bundleName", resolveBundleFromPath())
	pathBundleRoutes.GET("/tools", handleToolsList(db, aggregator, ""))
	pathBundleRoutes.POST("/mcp", handleBundleMCP(db, aggregator, bundleStore, skillStorage, ""))

	adminBundles := engine.Group("/v1/mcp/bundles", auth.BundleAdminMiddleware(strings.TrimSpace(os.Getenv("BUNDLE_ADMIN_TOKEN"))))
	adminBundles.POST("", handleUpsertBundle(bundleStore, true))
	adminBundles.PUT("/:bundleName", handleUpsertBundle(bundleStore, false))
	adminBundles.DELETE("/:bundleName", handleDeactivateBundle(bundleStore))

	return engine
}

func handleToolsList(db *sql.DB, aggregator *mcp.SchemaAggregator, fallbackBundleName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var allowedToolNames map[string]struct{}
		proof := strings.TrimSpace(c.GetHeader(auth.PaymentProofHeader))
		if proof != "" {
			resolvedToolNames, err := auth.LookupAuthorizedToolNames(c.Request.Context(), db, proof)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error":   "load_tool_permissions_failed",
					"message": "从 PostgreSQL 读取工具权限失败",
				})
				return
			}
			allowedToolNames = resolvedToolNames
		}

		tools, err := aggregator.GetAggregateTools(c.Request.Context(), mcp.ListToolsOptions{
			CursorContext:    c.Query("cursor_context"),
			BundleName:       resolveRequestedBundleName(c, fallbackBundleName),
			Limit:            mcp.DefaultSemanticLimit,
			AllowedToolNames: allowedToolNames,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "load_tools_failed",
				"message": "从 PostgreSQL 读取激活技能列表失败",
			})
			return
		}

		c.JSON(http.StatusOK, mcp.ToolsListResponse{Tools: tools})
	}
}

func resolveBundleFromHost() gin.HandlerFunc {
	return func(c *gin.Context) {
		bundleName := parseBundleNameFromHost(c.Request.Host)
		if bundleName == "" {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "bundle_not_found",
				"message": "当前 host 未匹配到 bundle",
			})
			return
		}

		c.Set(auth.RequestedBundleContextKey, bundleName)
		c.Next()
	}
}

func resolveBundleFromPath() gin.HandlerFunc {
	return func(c *gin.Context) {
		bundleName := strings.TrimSpace(c.Param("bundleName"))
		if bundleName == "" {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "bundle_not_found",
				"message": "缺少 bundle 路径参数",
			})
			return
		}

		c.Set(auth.RequestedBundleContextKey, bundleName)
		c.Next()
	}
}

func resolveRequestedBundleName(c *gin.Context, fallbackBundleName string) string {
	if c == nil {
		return strings.TrimSpace(fallbackBundleName)
	}

	bundleName := strings.TrimSpace(c.GetString(auth.RequestedBundleContextKey))
	if bundleName != "" {
		return bundleName
	}

	return strings.TrimSpace(fallbackBundleName)
}

func parseBundleNameFromHost(rawHost string) string {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return ""
	}

	if index := strings.Index(host, ":"); index >= 0 {
		host = host[:index]
	}

	const suffix = ".skillfun.ai"
	if !strings.HasSuffix(host, suffix) || strings.HasPrefix(host, "mcp.") {
		return ""
	}

	bundleName := strings.TrimSpace(strings.TrimSuffix(host, suffix))
	if bundleName == "" || strings.Contains(bundleName, ".") {
		return ""
	}

	return bundleName
}

type bundleUpsertRequest struct {
	BundleName  string               `json:"bundleName"`
	Subdomain   string               `json:"subdomain"`
	DisplayName string               `json:"displayName"`
	Description string               `json:"description"`
	ToolNames   []string             `json:"toolNames"`
	Skills      []bundleSkillRequest `json:"skills"`
	IsActive    *bool                `json:"isActive"`
}

type bundleSkillRequest struct {
	NFTID       int64           `json:"nftId"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	GitHubURL   string          `json:"githubUrl"`
}

func handleUpsertBundle(bundleStore *bundlepkg.Store, isCreate bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request bundleUpsertRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_bundle_payload",
				"message": "Bundle 管理请求体不是合法 JSON",
			})
			return
		}

		bundleName := strings.TrimSpace(request.BundleName)
		if !isCreate {
			bundleName = strings.TrimSpace(c.Param("bundleName"))
		}

		isActive := true
		if request.IsActive != nil {
			isActive = *request.IsActive
		}

		bundleMetadata, err := bundleStore.UpsertBundle(c.Request.Context(), bundlepkg.UpsertBundleInput{
			BundleName:  bundleName,
			Subdomain:   request.Subdomain,
			DisplayName: request.DisplayName,
			Description: request.Description,
			ToolNames:   request.ToolNames,
			Skills:      toManagedSkills(request.Skills),
			IsActive:    isActive,
		})
		if err != nil {
			switch {
			case errors.Is(err, bundlepkg.ErrInvalidSubdomain):
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "invalid_subdomain",
					"message": "subdomain 仅支持小写字母、数字、连字符，长度限制 8-16 个字符",
				})
			case errors.Is(err, bundlepkg.ErrSubdomainTaken):
				c.JSON(http.StatusConflict, gin.H{
					"error":   "subdomain_taken",
					"message": "subdomain 已被占用",
				})
			case errors.Is(err, bundlepkg.ErrSubdomainCooldown):
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":   "subdomain_cooldown",
					"message": "subdomain 修改冷却中，24 小时后可再次修改",
				})
			case errors.Is(err, bundlepkg.ErrSubdomainChangeCap):
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":   "subdomain_change_limit_reached",
					"message": "本月 subdomain 修改次数已达上限",
				})
			case errors.Is(err, bundlepkg.ErrUnknownSkill):
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "unknown_skill",
					"message": err.Error(),
				})
			case errors.Is(err, bundlepkg.ErrInvalidSkillPayload):
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "invalid_skill_payload",
					"message": err.Error(),
				})
			case errors.Is(err, bundlepkg.ErrSkillSyncFailed):
				c.JSON(http.StatusBadGateway, gin.H{
					"error":   "sync_skill_failed",
					"message": err.Error(),
				})
			case errors.Is(err, bundlepkg.ErrSkillAlreadyBundled):
				c.JSON(http.StatusConflict, gin.H{
					"error":   "skill_already_bundled",
					"message": err.Error(),
				})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{
					"error":   "save_bundle_failed",
					"message": "保存 bundle 失败",
				})
			}
			return
		}

		statusCode := http.StatusOK
		if isCreate {
			statusCode = http.StatusCreated
		}
		c.JSON(statusCode, gin.H{"bundle": bundleMetadata})
	}
}

func toManagedSkills(requestSkills []bundleSkillRequest) []bundlepkg.ManagedSkillInput {
	managedSkills := make([]bundlepkg.ManagedSkillInput, 0, len(requestSkills))
	for _, requestSkill := range requestSkills {
		managedSkills = append(managedSkills, bundlepkg.ManagedSkillInput{
			NFTID:       requestSkill.NFTID,
			Name:        requestSkill.Name,
			Description: requestSkill.Description,
			InputSchema: requestSkill.InputSchema,
			GitHubURL:   requestSkill.GitHubURL,
		})
	}

	return managedSkills
}

func handleDeactivateBundle(bundleStore *bundlepkg.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := bundleStore.DeactivateBundle(c.Request.Context(), c.Param("bundleName")); err != nil {
			if errors.Is(err, bundlepkg.ErrBundleNotFound) {
				c.JSON(http.StatusNotFound, gin.H{
					"error":   "bundle_not_found",
					"message": "未找到指定 bundle",
				})
				return
			}

			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "deactivate_bundle_failed",
				"message": "停用 bundle 失败",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "deactivated"})
	}
}

// newPostgresDB 根据环境变量初始化 PostgreSQL 连接。
func newPostgresDB() (*sql.DB, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("缺少必须的环境变量: DATABASE_URL")
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	return db, nil
}

// pingPostgres 在服务启动前主动探活 PostgreSQL，避免服务起来后才暴露依赖异常。
func pingPostgres(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), postgresPingTimeout)
	defer cancel()

	return db.PingContext(ctx)
}

// ensureSchema 初始化网关运行所需的最小 PostgreSQL 表结构。
func ensureSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS skills (
			nft_id BIGINT PRIMARY KEY,
			tool_name TEXT NOT NULL,
			upstream_url TEXT,
			schema_json JSONB NOT NULL,
			github_url TEXT,
			skill_dir_name TEXT,
			sync_status TEXT NOT NULL DEFAULT 'ready',
			last_synced_at TIMESTAMPTZ,
			sync_error TEXT,
			is_active BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`ALTER TABLE skills ADD COLUMN IF NOT EXISTS upstream_url TEXT`,
		`ALTER TABLE skills ADD COLUMN IF NOT EXISTS github_url TEXT`,
		`ALTER TABLE skills ADD COLUMN IF NOT EXISTS skill_dir_name TEXT`,
		`ALTER TABLE skills ADD COLUMN IF NOT EXISTS sync_status TEXT`,
		`ALTER TABLE skills ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ`,
		`ALTER TABLE skills ADD COLUMN IF NOT EXISTS sync_error TEXT`,
		`UPDATE skills SET sync_status = 'ready' WHERE sync_status IS NULL OR TRIM(sync_status) = ''`,
		`ALTER TABLE skills ALTER COLUMN sync_status SET DEFAULT 'ready'`,
		`ALTER TABLE skills ALTER COLUMN sync_status SET NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS skills_skill_dir_name_key ON skills(skill_dir_name) WHERE skill_dir_name IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS skills_tool_name_key ON skills(tool_name)`,
		`CREATE TABLE IF NOT EXISTS bundles (
			bundle_name TEXT PRIMARY KEY,
			subdomain TEXT,
			display_name TEXT NOT NULL,
			description TEXT,
			is_active BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`ALTER TABLE bundles ADD COLUMN IF NOT EXISTS route_key TEXT`,
		`ALTER TABLE bundles ADD COLUMN IF NOT EXISTS subdomain TEXT`,
		`UPDATE bundles
		SET subdomain = COALESCE(NULLIF(TRIM(subdomain), ''), NULLIF(TRIM(route_key), ''), bundle_name)
		WHERE subdomain IS NULL OR TRIM(subdomain) = ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS bundles_subdomain_unique ON bundles(subdomain)`,
		`CREATE TABLE IF NOT EXISTS bundle_subdomain_changes (
			id BIGSERIAL PRIMARY KEY,
			bundle_name TEXT NOT NULL REFERENCES bundles(bundle_name) ON DELETE CASCADE,
			old_subdomain TEXT NOT NULL,
			new_subdomain TEXT NOT NULL,
			changed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS bundle_subdomain_changes_bundle_name_idx ON bundle_subdomain_changes(bundle_name, changed_at DESC)`,
		`CREATE TABLE IF NOT EXISTS bundle_skills (
			bundle_name TEXT NOT NULL REFERENCES bundles(bundle_name) ON DELETE CASCADE,
			tool_name TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (bundle_name, tool_name)
		)`,
		`CREATE INDEX IF NOT EXISTS bundle_skills_tool_name_idx ON bundle_skills(tool_name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS bundle_skills_tool_name_unique ON bundle_skills(tool_name)`,
		`INSERT INTO bundles (bundle_name, display_name, description, is_active)
		SELECT DISTINCT
			CASE
				WHEN POSITION(':' IN tool_name) > 0 THEN split_part(tool_name, ':', 1)
				WHEN POSITION('/' IN tool_name) > 0 THEN split_part(tool_name, '/', 1)
			END AS bundle_name,
			CASE
				WHEN POSITION(':' IN tool_name) > 0 THEN split_part(tool_name, ':', 1)
				WHEN POSITION('/' IN tool_name) > 0 THEN split_part(tool_name, '/', 1)
			END AS display_name,
			NULL,
			TRUE
		FROM skills
		WHERE POSITION(':' IN tool_name) > 0 OR POSITION('/' IN tool_name) > 0
		ON CONFLICT (bundle_name) DO NOTHING`,
		`INSERT INTO bundle_skills (bundle_name, tool_name)
		SELECT DISTINCT
			CASE
				WHEN POSITION(':' IN tool_name) > 0 THEN split_part(tool_name, ':', 1)
				WHEN POSITION('/' IN tool_name) > 0 THEN split_part(tool_name, '/', 1)
			END AS bundle_name,
			tool_name
		FROM skills
		WHERE POSITION(':' IN tool_name) > 0 OR POSITION('/' IN tool_name) > 0
		ON CONFLICT (bundle_name, tool_name) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS payment_proofs (
			proof TEXT PRIMARY KEY,
			tool_name TEXT,
			grant_type TEXT,
			grant_target TEXT,
			expires_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`ALTER TABLE payment_proofs ADD COLUMN IF NOT EXISTS grant_type TEXT`,
		`ALTER TABLE payment_proofs ADD COLUMN IF NOT EXISTS grant_target TEXT`,
		`ALTER TABLE payment_proofs ALTER COLUMN tool_name DROP NOT NULL`,
		`UPDATE payment_proofs SET grant_type = 'tool' WHERE grant_type IS NULL`,
		`UPDATE payment_proofs SET grant_target = tool_name WHERE grant_target IS NULL AND tool_name IS NOT NULL`,
		`ALTER TABLE payment_proofs ALTER COLUMN grant_type SET DEFAULT 'tool'`,
		`ALTER TABLE payment_proofs ALTER COLUMN grant_type SET NOT NULL`,
		`ALTER TABLE payment_proofs ALTER COLUMN grant_target SET NOT NULL`,
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conname = 'payment_proofs_grant_type_check'
			) THEN
				ALTER TABLE payment_proofs
				ADD CONSTRAINT payment_proofs_grant_type_check
				CHECK (grant_type IN ('tool', 'bundle'));
			END IF;
		END
		$$`,
		`CREATE INDEX IF NOT EXISTS payment_proofs_grant_lookup_idx ON payment_proofs(proof, grant_type, grant_target)`,
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("exec schema statement: %w", err)
		}
	}

	return nil
}
