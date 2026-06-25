package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"skillfun-mcp/internal/auth"
	bundlepkg "skillfun-mcp/internal/bundle"
	"skillfun-mcp/internal/mcp"
	"skillfun-mcp/internal/skills"
)

type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpInitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      mcpServerInfo  `json:"serverInfo"`
}

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListParams struct {
	CursorContext      string `json:"cursor_context"`
	CursorContextCamel string `json:"cursorContext"`
	Limit              int    `json:"limit"`
}

type resourcesReadParams struct {
	URI string `json:"uri"`
}

const resourceReadAuditInsertQuery = `
INSERT INTO skill_resource_read_events (
	bundle_name,
	bundle_subdomain,
	tool_name,
	skill_dir_name,
	resource_uri,
	resource_path,
	mcp_method,
	proof,
	grant_type,
	grant_target,
	client_user_agent,
	client_ip,
	rpc_request_id,
	success,
	http_status,
	rpc_error_code,
	error_message,
	mime_type,
	content_bytes,
	content_sha256
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
`

const resourceReadAuditBundleQuery = `
SELECT b.bundle_name, COALESCE(NULLIF(TRIM(b.subdomain), ''), b.bundle_name)
FROM bundles b
JOIN bundle_skills bs
	ON bs.bundle_name = b.bundle_name
WHERE bs.tool_name = $2
  AND (b.bundle_name = $1 OR b.subdomain = $1)
LIMIT 1
`

const resourceReadAuditGrantQuery = `
SELECT COALESCE(grant_type, 'tool'), COALESCE(grant_target, tool_name)
FROM payment_proofs
WHERE proof = $1
  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
LIMIT 1
`

type resourceReadAuditEvent struct {
	bundleName      string
	bundleSubdomain string
	toolName        string
	skillDirName    string
	resourceURI     string
	resourcePath    string
	proof           string
	grantType       string
	grantTarget     string
	clientUserAgent string
	clientIP        string
	rpcRequestID    string
	success         bool
	httpStatus      int
	rpcErrorCode    any
	errorMessage    any
	mimeType        any
	contentBytes    any
	contentSHA256   any
}

func handleBundleMCP(db *sql.DB, aggregator toolAggregator, bundleStore bundleStoreAPI, skillStorage skills.Storage, fallbackBundleName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if aggregator == nil {
			writeRPCResponse(c, http.StatusInternalServerError, nil, nil, &mcpRPCError{Code: -32603, Message: "mcp schema aggregator is unavailable"})
			return
		}
		if bundleStore == nil {
			writeRPCResponse(c, http.StatusInternalServerError, nil, nil, &mcpRPCError{Code: -32603, Message: "bundle store is unavailable"})
			return
		}

		var request mcpRPCRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			writeRPCResponse(c, http.StatusBadRequest, nil, nil, &mcpRPCError{Code: -32700, Message: "invalid json-rpc request"})
			return
		}
		if request.JSONRPC != "" && request.JSONRPC != "2.0" {
			writeRPCResponse(c, http.StatusBadRequest, request.ID, nil, &mcpRPCError{Code: -32600, Message: "jsonrpc must be 2.0"})
			return
		}
		isNotification := len(request.ID) == 0 && strings.HasPrefix(request.Method, "notifications/")
		respond := func(statusCode int, result any, rpcError *mcpRPCError) {
			if isNotification {
				c.Status(http.StatusNoContent)
				return
			}
			writeRPCResponse(c, statusCode, request.ID, result, rpcError)
		}

		bundleName := resolveRequestedBundleName(c, fallbackBundleName)
		switch request.Method {
		case "initialize":
			respond(http.StatusOK, mcpInitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities: map[string]any{
					"tools": map[string]any{
						"listChanged": false,
					},
					"resources": map[string]any{
						"subscribe":   false,
						"listChanged": false,
					},
				},
				ServerInfo: mcpServerInfo{
					Name:    "skillfun-bundle-mcp",
					Version: "1.0.0",
				},
			}, nil)
		case "notifications/initialized":
			respond(http.StatusNoContent, nil, nil)
		case "tools/list":
			params := toolsListParams{}
			if len(request.Params) > 0 {
				if err := json.Unmarshal(request.Params, &params); err != nil {
					respond(http.StatusBadRequest, nil, &mcpRPCError{Code: -32602, Message: "invalid tools/list params"})
					return
				}
			}

			allowedToolNames, err := lookupAllowedToolNames(c, db)
			if err != nil {
				respond(http.StatusInternalServerError, nil, &mcpRPCError{Code: -32603, Message: "failed to load tool permissions"})
				return
			}

			cursorContext := strings.TrimSpace(params.CursorContext)
			if cursorContext == "" {
				cursorContext = strings.TrimSpace(params.CursorContextCamel)
			}

			tools, err := aggregator.GetAggregateTools(c.Request.Context(), mcp.ListToolsOptions{
				CursorContext:    cursorContext,
				BundleName:       bundleName,
				Limit:            params.Limit,
				AllowedToolNames: allowedToolNames,
			})
			if err != nil {
				respond(http.StatusInternalServerError, nil, &mcpRPCError{Code: -32603, Message: "failed to load tools"})
				return
			}

			respond(http.StatusOK, mcp.ToolsListResponse{Tools: tools}, nil)
		case "resources/list":
			if skillStorage == nil {
				respond(http.StatusInternalServerError, nil, &mcpRPCError{Code: -32603, Message: "skill storage is unavailable"})
				return
			}

			allowedToolNames, err := lookupAllowedToolNames(c, db)
			if err != nil {
				respond(http.StatusInternalServerError, nil, &mcpRPCError{Code: -32603, Message: "failed to load tool permissions"})
				return
			}

			bindings, err := bundleStore.ListBundleResourceBindings(c.Request.Context(), bundleName, allowedToolNames)
			if err != nil {
				statusCode := http.StatusInternalServerError
				if errors.Is(err, bundlepkg.ErrBundleNotFound) {
					statusCode = http.StatusNotFound
				}
				respond(statusCode, nil, &mcpRPCError{Code: -32603, Message: "failed to list bundle resources"})
				return
			}

			var resources []mcp.MCPResource
			for _, binding := range bindings {
				skillResources, err := skillStorage.ListResources(binding.ToolName, binding.SkillDirName)
				if err != nil {
					respond(http.StatusInternalServerError, nil, &mcpRPCError{Code: -32603, Message: "failed to list skill resources"})
					return
				}
				resources = append(resources, skillResources...)
			}
			sort.Slice(resources, func(i, j int) bool {
				return resources[i].URI < resources[j].URI
			})

			respond(http.StatusOK, mcp.ResourcesListResponse{Resources: resources}, nil)
		case "resources/read":
			if skillStorage == nil {
				respond(http.StatusInternalServerError, nil, &mcpRPCError{Code: -32603, Message: "skill storage is unavailable"})
				return
			}

			var params resourcesReadParams
			if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.URI) == "" {
				respond(http.StatusBadRequest, nil, &mcpRPCError{Code: -32602, Message: "invalid resources/read params"})
				return
			}

			toolName, resourcePath, err := skills.ParseResourceURI(params.URI)
			if err != nil {
				respond(http.StatusBadRequest, nil, &mcpRPCError{Code: -32602, Message: err.Error()})
				return
			}

			allowedToolNames, err := lookupAllowedToolNames(c, db)
			if err != nil {
				respond(http.StatusInternalServerError, nil, &mcpRPCError{Code: -32603, Message: "failed to load tool permissions"})
				return
			}

			binding, err := bundleStore.GetBundleResourceBinding(c.Request.Context(), bundleName, toolName, allowedToolNames)
			if err != nil {
				statusCode := http.StatusInternalServerError
				code := -32603
				message := "failed to resolve skill resource"
				if errors.Is(err, bundlepkg.ErrBundleSkillNotFound) || errors.Is(err, bundlepkg.ErrBundleNotFound) {
					statusCode = http.StatusNotFound
					code = -32004
					message = "resource not found"
				}
				recordResourceReadEvent(c, db, request.ID, bundleName, resourceReadAuditEvent{
					toolName:     toolName,
					resourceURI:  params.URI,
					resourcePath: resourcePath,
					success:      false,
					httpStatus:   statusCode,
					rpcErrorCode: code,
					errorMessage: message,
				})
				respond(statusCode, nil, &mcpRPCError{Code: code, Message: message})
				return
			}

			content, err := skillStorage.ReadResource(binding.ToolName, binding.SkillDirName, params.URI)
			if err != nil {
				statusCode := http.StatusInternalServerError
				code := -32603
				message := "failed to read skill resource"
				if errors.Is(err, skills.ErrResourceNotFound) {
					statusCode = http.StatusNotFound
					code = -32004
					message = "resource not found"
				} else if errors.Is(err, skills.ErrInvalidResourceURI) || errors.Is(err, skills.ErrPathEscape) {
					statusCode = http.StatusBadRequest
					code = -32602
					message = err.Error()
				}
				recordResourceReadEvent(c, db, request.ID, bundleName, resourceReadAuditEvent{
					toolName:     binding.ToolName,
					skillDirName: binding.SkillDirName,
					resourceURI:  params.URI,
					resourcePath: resourcePath,
					success:      false,
					httpStatus:   statusCode,
					rpcErrorCode: code,
					errorMessage: message,
				})
				respond(statusCode, nil, &mcpRPCError{Code: code, Message: message})
				return
			}

			mimeType, contentBytes, contentSHA256 := resourceReadContentMetadata(content)
			recordResourceReadEvent(c, db, request.ID, bundleName, resourceReadAuditEvent{
				toolName:      binding.ToolName,
				skillDirName:  binding.SkillDirName,
				resourceURI:   params.URI,
				resourcePath:  resourcePath,
				success:       true,
				httpStatus:    http.StatusOK,
				mimeType:      mimeType,
				contentBytes:  contentBytes,
				contentSHA256: contentSHA256,
			})
			respond(http.StatusOK, mcp.ResourcesReadResponse{Contents: []mcp.MCPResourceContent{content}}, nil)
		default:
			respond(http.StatusNotFound, nil, &mcpRPCError{Code: -32601, Message: "method not found"})
		}
	}
}

func lookupAllowedToolNames(c *gin.Context, db *sql.DB) (map[string]struct{}, error) {
	proof := strings.TrimSpace(c.GetHeader(auth.PaymentProofHeader))
	if proof == "" {
		return nil, nil
	}

	return auth.LookupAuthorizedToolNames(c.Request.Context(), db, proof)
}

func writeRPCResponse(c *gin.Context, statusCode int, id json.RawMessage, result any, rpcError *mcpRPCError) {
	response := mcpRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcError,
	}
	c.JSON(statusCode, response)
}

func recordResourceReadEvent(c *gin.Context, db *sql.DB, requestID json.RawMessage, requestedBundleName string, event resourceReadAuditEvent) {
	if c == nil || c.Request == nil || db == nil {
		return
	}

	event.bundleName = strings.TrimSpace(requestedBundleName)
	event.bundleSubdomain = strings.TrimSpace(requestedBundleName)
	if event.bundleName == "" || strings.TrimSpace(event.toolName) == "" || strings.TrimSpace(event.resourceURI) == "" || strings.TrimSpace(event.resourcePath) == "" {
		return
	}

	if bundleName, bundleSubdomain, err := lookupResourceReadBundleIdentity(c.Request.Context(), db, requestedBundleName, event.toolName); err == nil {
		if strings.TrimSpace(bundleName) != "" {
			event.bundleName = strings.TrimSpace(bundleName)
		}
		if strings.TrimSpace(bundleSubdomain) != "" {
			event.bundleSubdomain = strings.TrimSpace(bundleSubdomain)
		}
	}

	event.proof = strings.TrimSpace(c.GetHeader(auth.PaymentProofHeader))
	if event.proof != "" {
		if grantType, grantTarget, err := lookupResourceReadGrantContext(c.Request.Context(), db, event.proof); err == nil {
			event.grantType = strings.TrimSpace(grantType)
			event.grantTarget = strings.TrimSpace(grantTarget)
		}
	}
	event.clientUserAgent = strings.TrimSpace(c.Request.UserAgent())
	event.clientIP = strings.TrimSpace(c.ClientIP())
	event.rpcRequestID = resourceReadRPCRequestID(requestID)

	_, _ = db.ExecContext(
		c.Request.Context(),
		resourceReadAuditInsertQuery,
		event.bundleName,
		event.bundleSubdomain,
		strings.TrimSpace(event.toolName),
		nullIfBlank(event.skillDirName),
		strings.TrimSpace(event.resourceURI),
		strings.TrimSpace(event.resourcePath),
		"resources/read",
		nullIfBlank(event.proof),
		nullIfBlank(event.grantType),
		nullIfBlank(event.grantTarget),
		nullIfBlank(event.clientUserAgent),
		nullIfBlank(event.clientIP),
		nullIfBlank(event.rpcRequestID),
		event.success,
		event.httpStatus,
		event.rpcErrorCode,
		event.errorMessage,
		event.mimeType,
		event.contentBytes,
		event.contentSHA256,
	)
}

func lookupResourceReadBundleIdentity(ctx context.Context, db *sql.DB, requestedBundleName string, toolName string) (string, string, error) {
	if db == nil {
		return "", "", sql.ErrConnDone
	}

	var bundleName string
	var bundleSubdomain string
	err := db.QueryRowContext(ctx, resourceReadAuditBundleQuery, strings.TrimSpace(requestedBundleName), strings.TrimSpace(toolName)).Scan(&bundleName, &bundleSubdomain)
	return bundleName, bundleSubdomain, err
}

func lookupResourceReadGrantContext(ctx context.Context, db *sql.DB, proof string) (string, string, error) {
	if db == nil {
		return "", "", sql.ErrConnDone
	}

	var grantType string
	var grantTarget string
	err := db.QueryRowContext(ctx, resourceReadAuditGrantQuery, strings.TrimSpace(proof)).Scan(&grantType, &grantTarget)
	return grantType, grantTarget, err
}

func resourceReadContentMetadata(content mcp.MCPResourceContent) (any, any, any) {
	mimeType := strings.TrimSpace(content.MimeType)

	var payload []byte
	switch {
	case content.Text != "":
		payload = []byte(content.Text)
	case content.Blob != "":
		decoded, err := base64.StdEncoding.DecodeString(content.Blob)
		if err == nil {
			payload = decoded
		}
	}

	if len(payload) == 0 {
		return nullIfBlank(mimeType), nil, nil
	}

	sum := sha256.Sum256(payload)
	return nullIfBlank(mimeType), int64(len(payload)), hex.EncodeToString(sum[:])
}

func resourceReadRPCRequestID(id json.RawMessage) string {
	return strings.TrimSpace(string(id))
}

func nullIfBlank(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	return value
}
