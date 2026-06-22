package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const RequestedBundleContextKey = "requested.bundleName"

const (
	PaymentProofHeader = "X-402-Payment-Proof"
	basePriceSKILL     = 100
	curatorMarkupSKILL = 20
)

const authorizedToolsByProofQuery = `
WITH active_grants AS (
	SELECT
		COALESCE(grant_type, 'tool') AS grant_type,
		COALESCE(grant_target, tool_name) AS grant_target
	FROM payment_proofs
	WHERE proof = $1
	  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
)
SELECT DISTINCT s.tool_name
FROM skills s
JOIN bundle_skills bs0
	ON bs0.tool_name = s.tool_name
JOIN bundles b0
	ON b0.bundle_name = bs0.bundle_name
   AND b0.is_active = TRUE
JOIN (
	SELECT grant_target AS tool_name
	FROM active_grants
	WHERE grant_type = 'tool'
	UNION
	SELECT bs.tool_name
	FROM active_grants ag
	JOIN bundles b
		ON ag.grant_type = 'bundle'
	   AND b.bundle_name = ag.grant_target
	   AND b.is_active = TRUE
	JOIN bundle_skills bs
		ON bs.bundle_name = b.bundle_name
) authorized
	ON authorized.tool_name = s.tool_name
WHERE s.is_active = TRUE
`

// toolRequestPayload 用于兼容直接请求体和 JSON-RPC 风格请求体中的 name 字段。
type toolRequestPayload struct {
	Name   string             `json:"name"`
	Params *toolRequestParams `json:"params,omitempty"`
}

// toolRequestParams 表示 JSON-RPC params 中的工具名称字段。
type toolRequestParams struct {
	Name string `json:"name"`
}

// paymentRequiredResponse 表示支付校验失败时返回给客户端的 402 响应体。
type paymentRequiredResponse struct {
	Error      string                 `json:"error"`
	Message    string                 `json:"message"`
	ToolName   string                 `json:"toolName"`
	BundleName string                 `json:"bundleName"`
	NFTID      string                 `json:"nftId"`
	Settlement map[string]interface{} `json:"settlement"`
}

// X402VerificationMiddleware 创建 Gin 中间件。
// 中间件启动时会注入 PostgreSQL 连接，后续支付凭证校验将复用该连接。
func X402VerificationMiddleware(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if db == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "payment_verification_unavailable",
				"message": "支付校验服务未正确初始化",
			})
			return
		}

		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request_body",
				"message": "读取请求体失败",
			})
			return
		}

		// 读取请求体后需要重新放回，避免影响后续 Handler 对 Body 的再次读取。
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		toolName, err := extractToolName(bodyBytes)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_tool_name",
				"message": err.Error(),
			})
			return
		}

		bundleName := strings.TrimSpace(c.GetString(RequestedBundleContextKey))
		nftID := ""
		if bundleName == "" {
			parsedBundleName, parsedNFTID, err := parseToolName(toolName)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"error":    "invalid_tool_name_format",
					"message":  err.Error(),
					"toolName": toolName,
				})
				return
			}
			bundleName = parsedBundleName
			nftID = parsedNFTID
		} else if parsedBundleName, parsedNFTID, err := parseToolName(toolName); err == nil {
			if parsedBundleName != bundleName {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"error":    "bundle_tool_mismatch",
					"message":  "请求中的 toolName 与当前 bundle base URL 不匹配",
					"toolName": toolName,
				})
				return
			}
			nftID = parsedNFTID
		}

		proof := strings.TrimSpace(c.GetHeader(PaymentProofHeader))
		if proof == "" {
			c.AbortWithStatusJSON(http.StatusPaymentRequired, paymentRequiredResponse{
				Error:      "payment_required",
				Message:    "缺少有效的 X-402 支付凭证，请先完成支付后再调用该 Agent。",
				ToolName:   toolName,
				BundleName: bundleName,
				NFTID:      nftID,
				Settlement: map[string]interface{}{
					"currency":       "SKILL",
					"basePrice":      basePriceSKILL,
					"curatorMarkup":  curatorMarkupSKILL,
					"totalPrice":     basePriceSKILL + curatorMarkupSKILL,
					"proofHeader":    PaymentProofHeader,
					"paymentChannel": "x402",
				},
			})
			return
		}

		valid, err := verifyPaymentProof(c.Request.Context(), db, proof, toolName)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "payment_verification_failed",
				"message": "校验 X-402 支付凭证失败",
			})
			return
		}

		if !valid {
			c.AbortWithStatusJSON(http.StatusPaymentRequired, paymentRequiredResponse{
				Error:      "payment_required",
				Message:    "缺少有效的 X-402 支付凭证，请先完成支付后再调用该 Agent。",
				ToolName:   toolName,
				BundleName: bundleName,
				NFTID:      nftID,
				Settlement: map[string]interface{}{
					"currency":       "SKILL",
					"basePrice":      basePriceSKILL,
					"curatorMarkup":  curatorMarkupSKILL,
					"totalPrice":     basePriceSKILL + curatorMarkupSKILL,
					"proofHeader":    PaymentProofHeader,
					"paymentChannel": "x402",
				},
			})
			return
		}

		// 将解析结果写入上下文，便于后续业务处理复用。
		c.Set("x402.toolName", toolName)
		c.Set("x402.bundleName", bundleName)
		c.Set("x402.nftId", nftID)

		// 再次恢复请求体，确保后续处理链可以正常读取。
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		c.Next()
	}
}

// NewX402Middleware 保留为兼容入口，内部复用 X402VerificationMiddleware。
func NewX402Middleware(db *sql.DB) gin.HandlerFunc {
	return X402VerificationMiddleware(db)
}

// extractToolName 从请求体中提取工具名称。
// 这里兼容两种常见格式：
// 1. {"name":"bundle:nftId"}
// 2. {"params":{"name":"bundle:nftId"}}
func extractToolName(body []byte) (string, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", fmt.Errorf("请求体不能为空")
	}

	var payload toolRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("解析请求体 JSON 失败: %w", err)
	}

	if name := strings.TrimSpace(payload.Name); name != "" {
		return name, nil
	}

	if payload.Params != nil {
		if name := strings.TrimSpace(payload.Params.Name); name != "" {
			return name, nil
		}
	}

	return "", fmt.Errorf("请求体中缺少工具名称字段 name")
}

// parseToolName 从工具名称中拆分 bundleName 与 nftId。
// 这里约定工具名称格式为 bundleName:nftId，同时兼容 bundleName/nftId。
func parseToolName(toolName string) (string, string, error) {
	trimmedName := strings.TrimSpace(toolName)
	if trimmedName == "" {
		return "", "", fmt.Errorf("工具名称不能为空")
	}

	for _, separator := range []string{":", "/"} {
		left, right, found := strings.Cut(trimmedName, separator)
		if !found {
			continue
		}

		bundleName := strings.TrimSpace(left)
		nftID := strings.TrimSpace(right)
		if bundleName == "" || nftID == "" {
			return "", "", fmt.Errorf("工具名称格式非法，bundleName 或 nftId 不能为空")
		}

		return bundleName, nftID, nil
	}

	return "", "", fmt.Errorf("工具名称格式非法，期望格式为 bundleName:nftId")
}

// verifyPaymentProof 校验支付凭证是否有效且属于当前 Agent。
func verifyPaymentProof(ctx context.Context, db *sql.DB, proof string, toolName string) (bool, error) {
	authorizedToolNames, err := LookupAuthorizedToolNames(ctx, db, proof)
	if err != nil {
		return false, err
	}

	_, ok := authorizedToolNames[strings.TrimSpace(toolName)]
	return ok, nil
}

// LookupAuthorizedToolNames 查询当前支付凭证允许访问的工具集合。
func LookupAuthorizedToolNames(ctx context.Context, db *sql.DB, proof string) (map[string]struct{}, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres db is nil")
	}

	proof = strings.TrimSpace(proof)
	if proof == "" {
		return map[string]struct{}{}, nil
	}

	rows, err := db.QueryContext(ctx, authorizedToolsByProofQuery, proof)
	if err != nil {
		return nil, fmt.Errorf("query payment proof: %w", err)
	}
	defer rows.Close()

	authorizedToolNames := make(map[string]struct{})
	for rows.Next() {
		var toolName string
		if err := rows.Scan(&toolName); err != nil {
			return nil, fmt.Errorf("scan authorized tool: %w", err)
		}

		authorizedToolNames[strings.TrimSpace(toolName)] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authorized tools: %w", err)
	}

	return authorizedToolNames, nil
}
