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

// MCPResource 表示 MCP 资源列表中的单个资源。
type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	MimeType    string `json:"mimeType,omitempty"`
	Description string `json:"description,omitempty"`
}

// ResourcesListResponse 表示 MCP resources/list 接口的返回结果。
type ResourcesListResponse struct {
	Resources []MCPResource `json:"resources"`
}

// MCPResourceContent 表示 resources/read 的单个内容项。
type MCPResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ResourcesReadResponse 表示 MCP resources/read 接口的返回结果。
type ResourcesReadResponse struct {
	Contents []MCPResourceContent `json:"contents"`
}
