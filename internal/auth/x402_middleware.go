package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	paymentProofHeader       = "X-402-Payment-Proof"
	paymentProofRedisKeyPref = "skillfun:x402:payment:"
	basePriceSKILL           = 100
	curatorMarkupSKILL       = 20
)

var paymentRedisClient *redis.Client

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
// 中间件启动时会注入 Redis 客户端，后续支付凭证校验将复用该客户端。
func X402VerificationMiddleware(redisClient *redis.Client) gin.HandlerFunc {
	paymentRedisClient = redisClient

	return func(c *gin.Context) {
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

		bundleName, nftID, err := parseToolName(toolName)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":    "invalid_tool_name_format",
				"message":  err.Error(),
				"toolName": toolName,
			})
			return
		}

		proof := strings.TrimSpace(c.GetHeader(paymentProofHeader))
		if proof == "" || !verifyPaymentInRedis(proof, toolName) {
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
					"proofHeader":    paymentProofHeader,
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
func NewX402Middleware(redisClient *redis.Client) gin.HandlerFunc {
	return X402VerificationMiddleware(redisClient)
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

// verifyPaymentInRedis 模拟校验支付凭证是否有效且属于当前 Agent。
// 这里使用 Redis String 做最小实现：
// key   = skillfun:x402:payment:<proof>
// value = <toolName>
func verifyPaymentInRedis(proof string, toolName string) bool {
	if paymentRedisClient == nil {
		return false
	}

	proof = strings.TrimSpace(proof)
	toolName = strings.TrimSpace(toolName)
	if proof == "" || toolName == "" {
		return false
	}

	boundToolName, err := paymentRedisClient.Get(context.Background(), paymentProofRedisKeyPref+proof).Result()
	if err != nil {
		return false
	}

	return strings.TrimSpace(boundToolName) == toolName
}
