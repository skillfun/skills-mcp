package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/redis/go-redis/v9"
)

const activeToolsRedisKey = "skillfun:active_tools"

// SchemaAggregator 负责从 Redis 聚合当前激活的 MCP Tool Schema。
type SchemaAggregator struct {
	client *redis.Client
}

// NewSchemaAggregator 创建一个新的 SchemaAggregator。
func NewSchemaAggregator(client *redis.Client) *SchemaAggregator {
	return &SchemaAggregator{client: client}
}

// GetAggregateTools 从 Redis 的 Hash 键 skillfun:active_tools 中读取全部工具定义，
// 并将每个字段值中的 JSON 数据反序列化为 MCPTool。
func (a *SchemaAggregator) GetAggregateTools(ctx context.Context) ([]MCPTool, error) {
	if a == nil {
		return nil, fmt.Errorf("schema aggregator is nil")
	}

	if a.client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}

	entries, err := a.client.HGetAll(ctx, activeToolsRedisKey).Result()
	if err != nil {
		return nil, fmt.Errorf("read active tools from redis: %w", err)
	}

	// 对 Hash 字段排序，保证返回结果稳定，便于调试和测试。
	fields := make([]string, 0, len(entries))
	for field := range entries {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	tools := make([]MCPTool, 0, len(entries))
	for _, field := range fields {
		var tool MCPTool
		if err := json.Unmarshal([]byte(entries[field]), &tool); err != nil {
			return nil, fmt.Errorf("unmarshal tool %q: %w", field, err)
		}

		tools = append(tools, tool)
	}

	return tools, nil
}
