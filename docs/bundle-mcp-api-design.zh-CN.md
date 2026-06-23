# Bundle MCP API 设计

## 目标

实现一个 **按 bundle 作用域隔离的 MCP server**，满足：

- 每个 bundle 对外暴露为一个独立的 MCP server；
- bundle 元数据存放在 PostgreSQL 中；
- 每个已注册 skill 在注册时从 **public GitHub URL** 同步到本地文件系统目录；
- MCP 将已注册 skill 目录中的文件作为资源暴露，不解释其业务语义。

## 架构概览

### Bundle

- bundle 元数据仅存储在 PostgreSQL 中；
- bundle **不**在磁盘上拥有独立目录；
- bundle 仍然是对外 MCP server 的边界。

### Skill

- 每个 skill 在 PostgreSQL 中归属于一个 bundle；
- 每个 skill 在注册或更新时被物化到本地文件系统目录；
- 运行时不会通过扫描磁盘来发现 skill；
- 运行时以 PostgreSQL 作为 skill 存在性和归属关系的唯一真实来源。

### 资源模型

- `tool = skill`
- `resource = 已注册 skill 目录中的一个文件`

资源 URI 示例：

```text
skillfun://skills/{skillName}/files/{relativePath}
```

## 存储模型

### PostgreSQL

bundle 记录继续存放在 `bundles` 表中。

除了当前已有的 `nft_id`、`tool_name` 和 `schema_json` 外，skill 记录还需要补充 Git 同步相关元数据。

对外 API 只接收一个 `githubUrl`，服务端在校验后可以把它解析并持久化为标准化字段。

建议新增字段：

| 字段 | 作用 |
| --- | --- |
| `bundle_name` 或通过 `bundle_skills` 建立关联 | bundle 归属关系 |
| `tool_name` | MCP tool / skill 名称 |
| `skill_dir_name` | 规范化后的、路径安全的目录名 |
| `github_url` | 管理 API 传入的原始 public GitHub URL |
| `github_repo` | 从 URL 解析出的标准化 `owner/repo` |
| `github_ref` | 从 URL 解析出的标准化 branch、tag 或 commit |
| `github_subpath` | 从 URL 解析出的标准化子目录路径 |
| `sync_status` | `pending`、`ready`、`failed` |
| `last_synced_at` | 最近一次成功同步时间 |
| `sync_error` | 最近一次同步失败信息，供运维/管理端查看 |

### 文件系统

文件系统仅保存 skill 内容目录。

项目使用一个共享环境变量作为所有 skill 的根目录，例如：

```text
SKILL_STORAGE_ROOT=/var/lib/skillfun/skills
```

推荐的实际目录结构：

```text
${SKILL_STORAGE_ROOT}/{skill_dir_name}/
```

这里刻意 **没有** bundle 目录这一层。

## Skill 目录名规则

skill 目录名由 skill 名称推导，并持久化在 PostgreSQL 中。

规范化规则：

1. 去掉首尾空白；
2. 将空格统一转换为 `_`；
3. 替换或移除路径中不安全的字符；
4. 将结果保存为 `skill_dir_name`。

示例：

| Skill 名称 | 目录名 |
| --- | --- |
| `Current Weather` | `Current_Weather` |
| `foo/bar` | `foo_bar` |
| `a:b*c` | `a_b_c` |

重名处理：

1. 先尝试使用规范化后的基础名；
2. 若已被占用，则追加稳定后缀；
3. 将最终选定的目录名持久化到 PostgreSQL。

推荐后缀策略：

```text
{normalized}__{skill_id}
```

这样可以避免重试时目录名漂移。

## 注册与同步流程

skill 注册不仅仅是写 DB，还必须包含 Git 同步。

### 注册流程

1. 校验 bundle 存在且允许更新；
2. 校验 skill 请求体；
3. 将 `skill_name` 规范化为 `skill_dir_name`；
4. 处理命名冲突；
5. 插入或更新 skill 行，并先置 `sync_status = pending`；
6. 解析 GitHub URL，并拉取其中指定的 repo/ref/subpath；
7. 将内容写入 `SKILL_STORAGE_ROOT` 下的临时目录；
8. 将临时目录原子替换到最终 skill 目录；
9. 更新 skill 记录：
   - `sync_status = ready`
   - `last_synced_at`
10. 只有状态为 `ready` 的 skill 才对 MCP 可见。

### 失败处理

- Git 拉取失败时，该 skill 不应暴露给 MCP；
- 同步失败时将 `sync_status` 置为 `failed`；
- 必须清理不完整的临时目录；
- 若刷新同步失败，之前已就绪的内容应保持可用。

### 磁盘占用优化

推荐的存储策略是：只保留 **最终 skill 快照**，不要长期保留完整 Git 工作副本。

建议规则：

1. 只拉取 `githubUrl` 中解析出的目标子目录，不要保留整个仓库工作树；
2. 如果可行，优先使用 archive/export 这种“快照式”拉取；
3. 如果必须使用 Git clone，优先采用浅拉取和稀疏拉取，例如：
   - `--depth=1`
   - `--filter=blob:none`
   - `--sparse`
4. 发布后的 skill 目录中不能保留 `.git` 元数据；
5. 成功切换后，磁盘上默认只保留当前 `ready` 版本；
6. 发布成功后，立刻清理临时目录和下载归档包；
7. 后续如果多个 skill 指向相同 `repo + commit + subpath`，可再增加内容复用能力。

这样可以让运行时磁盘主要用于保存 MCP 可读的文件，而不是 Git 历史。

## 运行时查找模型

### 不基于磁盘做发现

服务端不应通过爬磁盘来发现 bundle 或 skill。

运行时的发现来源：

- bundles：PostgreSQL
- skills：PostgreSQL
- 针对某个已知 skill 的资源文件枚举：通过 `${SKILL_STORAGE_ROOT}/{skill_dir_name}` 计算出的目录

### 运行时访问模式

1. 从 host 或 path 解析 bundle；
2. 从 PostgreSQL 查询该 bundle 下的 active skill；
3. 需要访问文件时，只在该 skill 计算得到的目录中读取。

## MCP 对外面

## 外部入口

推荐的标准入口：

```text
https://{bundle}.skillfun.ai/mcp
```

备用/内部入口：

```text
https://gateway.skillfun.ai/{bundle}/mcp
```

传输方式：

- Streamable HTTP
- `POST` 必需
- `GET` 可选，MVP 可以直接返回 `405`
- `DELETE` 可选

## 支持的 MCP 方法

MVP 支持的方法：

1. `initialize`
2. `tools/list`
3. `resources/list`
4. `resources/read`

MVP 不包含：

- `tools/call`
- `resources/subscribe`
- `resources/templates/list`
- `prompts/*`

## 方法语义

### `tools/list`

从 PostgreSQL 返回当前 bundle 下所有 active skill。

每个 skill 对应一个 tool，包含：

- `name`
- `description`
- `inputSchema`

这里不依赖运行时磁盘发现。

### `resources/list`

返回当前 bundle 下所有已注册 skill 的文件资源。

对每个 skill：

1. 从 PostgreSQL 读取 `skill_dir_name`；
2. 通过 `SKILL_STORAGE_ROOT + skill_dir_name` 计算 skill 目录；
3. 枚举目录中的文件；
4. 将每个文件转换为 MCP resource。

每个文件对应一个 resource。

示例：

```json
{
  "resources": [
    {
      "uri": "skillfun://skills/current/files/prompt.md",
      "name": "current/prompt.md",
      "title": "current: prompt.md",
      "mimeType": "text/markdown",
      "description": "File resource in skill current"
    }
  ]
}
```

### `resources/read`

读取某个已注册 skill 目录下的文件。

流程：

1. 从资源 URI 中解析 `skillName` 与 `relativePath`；
2. 在当前 bundle 作用域内，从 PostgreSQL 解析出该 skill 行；
3. 读取 `skill_dir_name`；
4. 通过 `SKILL_STORAGE_ROOT + skill_dir_name` 计算 skill 目录；
5. 校验请求路径必须位于该 skill 根目录内；
6. 读取文件；
7. 按文件原始类型返回：
   - 文本文件返回 `text`
   - 二进制文件返回 `blob`
   - 始终附带 `mimeType`

文本示例：

```json
{
  "contents": [
    {
      "uri": "skillfun://skills/current/files/prompt.md",
      "mimeType": "text/markdown",
      "text": "# Prompt"
    }
  ]
}
```

二进制示例：

```json
{
  "contents": [
    {
      "uri": "skillfun://skills/current/files/icon.png",
      "mimeType": "image/png",
      "blob": "<base64>"
    }
  ]
}
```

## 管理 API 提案

当前已存在的 bundle 管理接口：

- `POST /v1/mcp/bundles`
- `PUT /v1/mcp/bundles/:bundleName`
- `DELETE /v1/mcp/bundles/:bundleName`

skill 请求体应从“内联内容”演进为“GitHub 驱动的同步来源”。

### 创建或更新 bundle

```http
POST /v1/mcp/bundles
Content-Type: application/json
Authorization: Bearer <bundle-admin-token>
```

```json
{
  "bundleName": "weather",
  "subdomain": "weatherhub",
  "displayName": "Weather Bundle",
  "description": "Weather skills",
  "isActive": true,
  "skills": [
    {
      "nftId": 1001,
      "name": "Current Weather",
      "description": "Get current weather",
      "inputSchema": {
        "type": "object",
        "properties": {
          "city": { "type": "string" }
        },
        "required": ["city"]
      },
      "githubUrl": "https://github.com/example/weather-skill/tree/main/skills/current"
    }
  ]
}
```

### 建议的 skill 请求字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `nftId` | 是 | skill 所有权引用 |
| `name` | 是 | 逻辑 skill/tool 名称 |
| `description` | 是 | tool 摘要 |
| `inputSchema` | 是 | MCP tool 输入 schema |
| `githubUrl` | 是 | 指向 skill 目录或快照根的 public GitHub URL |

### 建议的 bundle 响应扩展

成功响应可以保留现有 `bundle` 对象；如果后续需要，也可以增加 skill 同步状态信息。

示例：

```json
{
  "bundle": {
    "bundleName": "weather",
    "subdomain": "weatherhub",
    "displayName": "Weather Bundle",
    "description": "Weather skills",
    "isActive": true
  }
}
```

如果后续需要，可以增加一个仅管理员可见的详情接口来返回：

- `skillDirName`
- `syncStatus`
- `lastSyncedAt`
- `syncError`

## 校验规则

### Bundle 级

- `bundleName` 必填；
- `displayName` 必填；
- `subdomain` 必须符合当前仓库既有规则；
- 只有 active bundle 才会对外暴露。

### Skill 级

- `name` 必填；
- `description` 必填；
- `inputSchema` 必须是合法 JSON；
- `githubUrl` 必须是受支持的 public GitHub URL；
- `githubUrl` 必须能解析出 repo、ref 和有效子路径；
- `skill_dir_name` 由 `name` 按路径安全规则推导；
- 存储根目录来自共享环境变量，而不是数据库；
- 重名必须按确定性规则去重。

## 安全约束

### GitHub 来源约束

- MVP 仅支持 public GitHub；
- 限制允许的 URL scheme；
- 拒绝本地路径和不安全传输方式；
- 将 Git 内容视为不可信输入。

### 存储优化约束

- 发布后的 skill 目录应只包含最终资源快照；
- 发布目录中不能保留 `.git` 元数据；
- 默认不长期保留旧的 inactive 快照；
- 同步后的临时拉取和解压产物必须删除。

### 文件系统约束

- 禁止 `..` 路径穿越；
- 禁止在资源请求中使用绝对路径；
- 解析并拒绝跳出 skill 根目录的符号链接；
- 只暴露 `${SKILL_STORAGE_ROOT}/{skill_dir_name}` 下的文件；
- 默认忽略 `.git`、隐藏文件和临时产物。

## 错误模型

### 管理 API

- 请求体非法 → `400`
- 命名冲突且无法解决 → `409`
- 注册/更新过程中 Git 同步失败 → `500` 或显式同步失败状态
- bundle 不存在 → `404`

### MCP

- 未知方法 → JSON-RPC `-32601`
- 非法参数 → JSON-RPC `-32602`
- skill 或文件不存在 → 应用级 not found 错误
- 文件访问超出 skill 根目录 → forbidden / invalid params

## 任务拆分

### Phase 1：Schema 与模型

1. 扩展 skill 的 Git 同步元数据；
2. 持久化 `skill_dir_name`；
3. 增加同步状态字段。

### Phase 2：注册时同步

1. 扩展 bundle 管理 API 请求体；
2. 校验 `githubUrl` 及其解析出的 repo/ref/subpath；
3. 实现 skill 名规范化与冲突处理；
4. 实现 Git 拉取与临时目录原子替换；
5. 只有同步成功后才将 skill 标记为 `ready`。

### Phase 3：MCP 读取面

1. 实现 bundle 级 `initialize`；
2. 实现 DB 驱动的 `tools/list`；
3. 实现基于文件系统的 `resources/list`；
4. 实现基于文件系统的 `resources/read`。

### Phase 4：加固

1. 路径穿越与符号链接保护；
2. 隐藏文件过滤；
3. 结构化同步错误输出；
4. 重名、同步失败和资源访问规则测试。

## 说明

- 本文档描述的是目标 API 与存储模型，而不只是当前实现。
- 它与最新的产品决策保持一致：bundle 身份由数据库驱动，skill 内容以 Git 同步并落盘。
