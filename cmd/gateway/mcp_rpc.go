package main

import (
	"database/sql"
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

			toolName, _, err := skills.ParseResourceURI(params.URI)
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
				respond(statusCode, nil, &mcpRPCError{Code: code, Message: message})
				return
			}

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
