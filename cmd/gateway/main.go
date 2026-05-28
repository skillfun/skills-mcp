package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"skillfun-mcp/internal/auth"
	"skillfun-mcp/internal/mcp"
)

const (
	defaultRedisAddr = "localhost:6379"
	serverAddr       = ":8080"
	redisPingTimeout = 5 * time.Second
	shutdownTimeout  = 10 * time.Second
)

func main() {
	redisClient, err := newRedisClient()
	if err != nil {
		log.Fatalf("初始化 Redis 客户端失败: %v", err)
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Printf("关闭 Redis 客户端失败: %v", err)
		}
	}()

	if err := pingRedis(redisClient); err != nil {
		log.Fatalf("连接 Redis 失败: %v", err)
	}

	aggregator := mcp.NewSchemaAggregator(redisClient)

	engine := gin.New()
	engine.Use(
		gin.Logger(),
		gin.Recovery(),
		cors.New(cors.Config{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{
				http.MethodGet,
				http.MethodPost,
				http.MethodOptions,
			},
			AllowHeaders: []string{
				"Origin",
				"Content-Type",
				"Accept",
				"Authorization",
				"X-402-Payment-Proof",
			},
			ExposeHeaders: []string{"Content-Length"},
			MaxAge:        12 * time.Hour,
		}),
	)

	// 对外暴露当前 Redis 中所有已激活的 MCP 工具列表。
	engine.GET("/v1/mcp/tools", func(c *gin.Context) {
		tools, err := aggregator.GetAggregateTools(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "load_tools_failed",
				"message": "从 Redis 读取激活技能列表失败",
			})
			return
		}

		c.JSON(http.StatusOK, mcp.ToolsListResponse{Tools: tools})
	})

	// 具体工具调用入口，必须先经过 X-402 支付校验中间件。
	engine.POST("/v1/mcp/tools/call", auth.X402VerificationMiddleware(redisClient), handleToolCall)

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

// newRedisClient 根据环境变量初始化 Redis 客户端。
// 未显式配置时，默认连接本地 Redis: localhost:6379 / DB 0。
func newRedisClient() (*redis.Client, error) {
	db := 0
	if rawDB := os.Getenv("REDIS_DB"); rawDB != "" {
		parsedDB, err := strconv.Atoi(rawDB)
		if err != nil {
			return nil, fmt.Errorf("解析 REDIS_DB 失败: %w", err)
		}
		db = parsedDB
	}

	client := redis.NewClient(&redis.Options{
		Addr:     getenvOrDefault("REDIS_ADDR", defaultRedisAddr),
		Username: os.Getenv("REDIS_USERNAME"),
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       db,
	})

	return client, nil
}

// pingRedis 在服务启动前主动探活 Redis，避免服务起来后才暴露依赖异常。
func pingRedis(client *redis.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), redisPingTimeout)
	defer cancel()

	return client.Ping(ctx).Err()
}

// handleToolCall 是工具调用的网关入口。
// 当前版本主要负责接收请求并完成计费拦截后的受理，后续可在此接入真实的转发引擎。
func handleToolCall(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_request_body",
			"message": "读取工具调用请求体失败",
		})
		return
	}

	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "empty_request_body",
			"message": "工具调用请求体不能为空",
		})
		return
	}

	if !json.Valid(bodyBytes) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_json",
			"message": "工具调用请求体不是合法 JSON",
		})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"status":     "accepted",
		"message":    "工具调用已通过 X-402 计费校验，网关已受理该请求。",
		"toolName":   c.GetString("x402.toolName"),
		"bundleName": c.GetString("x402.bundleName"),
		"nftId":      c.GetString("x402.nftId"),
		"payload":    json.RawMessage(bodyBytes),
	})
}

// getenvOrDefault 读取环境变量；若为空则返回默认值。
func getenvOrDefault(key string, defaultValue string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}

	return defaultValue
}
