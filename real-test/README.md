# Real opencode MCP test

## Target

- MCP server name in opencode: `siteui25-local`
- MCP URL: `http://127.0.0.1:8080/siteui25/mcp`
- Bundle: `website-ui`
- Subdomain: `siteui25`

## Skills under test

1. `web-design-engineer`
   - `https://github.com/ConardLi/garden-skills/tree/main/skills/web-design-engineer`
2. `pma-design`
   - `https://github.com/zzci/skills/tree/main/skills/pma-design`

这两组都是实际的 design skill 目录，不是示例页目录。

## Clean workspace

本次 opencode 运行目录不是当前仓库，而是一个单独的干净目录：

- `opencode-real-test-workspace-3`（会话外部临时工作目录）

opencode 在该目录中生成了页面，随后我把产物复制到本仓库的 `real-test/`。

## Prompt style

给 opencode 的最终提示是普通用户角度的建站需求，没有包含具体的 MCP server 名称、资源 URI 或手工指定的 skill 名称。提示里只要求：

1. 做一个 AI 技能聚合与分发平台官网首页
2. 不要直接调用现成页面生成工具
3. 如果能访问到资源列表，先浏览并阅读最相关内容
4. 再在当前目录生成单页 HTML

## What opencode actually did

根据 `opencode-output.jsonl`，成功那次运行里 opencode 依次做了这些事：

1. `list_mcp_resources`
2. `list_mcp_resource_templates`
3. `read_mcp_resource` × 4
4. `write`

它实际读取到的资源来自 `web-design-engineer`：

- `README.md`
- `references/style-recipes/vercel-mesh.md`
- `references/style-recipes/linear.md`
- `references/style-recipes/raycast.md`

## Result

- MCP resource listing: **success**
- MCP resource reads: **success**
- Gateway audit rows recorded with user agent: `opencode/1.17.10`
- Generated page copied to: `real-test/generated-index.html`
- Raw opencode event stream copied to: `real-test/opencode-output.jsonl`
- Final opencode natural-language output copied to: `real-test/opencode-final-output.txt`

## Files

- `opencode-commands.txt` — CLI 调用记录
- `opencode-output.jsonl` — 成功那次运行的原始事件流
- `opencode-final-output.txt` — opencode 最终输出内容
- `generated-index.html` — opencode 生成的页面
- `gateway-audit.tsv` — 网关资源读取审计
