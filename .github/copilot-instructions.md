# SkillFun 统一 MCP 路由网关开发规范

## 1. 技术栈
- 语言：Go 语言 (使用 Gin 框架)
- 存储：PostgreSQL (存储 Skill Schema、上游路由、Bundle/Skill 关系、x402 支付状态)
- Web3：EVM RPC 调用 (用于技能注册时校验链上所有权)

## 2. 目录结构
/skillfun-mcp
├── /cmd/gateway        # 程序入口
├── /config             # 配置文件
├── /internal
│   ├── /mcp            # MCP 协议与 Schema 动态聚合
│   ├── /auth           # x402 协议支付拦截
│   └── /router         # 动态路由转发引擎
└── go.mod

## 3. 核心设计原则
- 所有的 MCP 接口必须符合 Model Context Protocol 官方规范。
- 必须预留完整的错误处理（Error Handling），禁止硬编码。
