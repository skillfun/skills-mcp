package mcp

// MCPTool 表示一个符合 Model Context Protocol 规范的工具定义。
// InputSchema 使用通用的 JSON 对象结构承载工具输入参数的 Schema。
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolsListResponse 表示 MCP tools/list 接口的返回结果。
type ToolsListResponse struct {
	Tools []MCPTool `json:"tools"`
}
