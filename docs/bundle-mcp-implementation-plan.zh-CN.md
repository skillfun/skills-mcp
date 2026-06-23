# Bundle MCP 实施计划

## 目标

实现一个按 bundle 作用域隔离的 MCP server，满足：

- bundle 元数据和路由保存在 PostgreSQL 中；
- 每个已注册 skill 在注册时从 public GitHub URL 同步到本地磁盘；
- MCP 对外暴露已注册 skill 的文件资源；
- 运行时不通过扫描磁盘来发现 skill。

这份计划文档是 `docs/bundle-mcp-api-design.md` 的实施配套文档。

## 已锁定决策

### Bundle 边界

- 每个 bundle 对外暴露为一个独立的 MCP server；
- bundle 在磁盘上没有专属目录；
- bundle 元数据继续保存在 PostgreSQL 中。

### Skill 存储

- 每个 skill 对应一个本地目录；
- 目录名由 skill 名称推导；
- 空格转为下划线；
- 仅允许路径安全字符；
- 重名必须去重，并将最终目录名持久化。

### 存储根目录

- 项目使用一个共享环境变量作为所有 skill 的根目录；
- 示例：

```text
SKILL_STORAGE_ROOT=/var/lib/skillfun/skills
```

- skill 的实际目录通过下面方式计算：

```text
${SKILL_STORAGE_ROOT}/{skill_dir_name}
```

### 同步来源

- skill 内容在注册/更新时同步；
- 当前 MVP 仅支持 public GitHub URL；
- 运行时 MCP 直接读取已经同步到本地的目录。
- 发布目录应是最终快照，而不是长期保留的完整 Git 工作副本；
- 发布目录中必须移除 `.git` 元数据；
- 发布成功后，默认只保留当前 `ready` 版本。

## 交付物

- [x] 支持 GitHub 驱动的 skill 同步的 schema 更新
- [x] 支持 GitHub 来源字段的注册 API 扩展
- [x] skill 名称规范化与重名处理
- [x] 注册时 Git 同步流程
- [x] bundle 级 MCP `initialize`、`tools/list`、`resources/list`、`resources/read`
- [x] 路径安全、同步失败和命名冲突相关校验与测试

## 工作拆分

## Phase 1：Schema 与模型更新

### 目标

让 PostgreSQL 成为 bundle 归属、skill 注册信息和同步状态的唯一真实来源。

### 任务

- [x] 为 skill 模型扩展 GitHub 同步元数据：
  - `github_url`
  - 可选的解析字段 `github_repo`
  - 可选的解析字段 `github_ref`
  - 可选的解析字段 `github_subpath`
  - `skill_dir_name`
  - `sync_status`
  - `last_synced_at`
  - `sync_error`
- [x] 决定这些字段直接放在 `skills` 表，还是放到关联表
- [x] 增加或更新查询与 store 方法
- [x] 确保只有 `ready` 的 skill 会通过 MCP 暴露

### 完成标准

- 可以在不保存每条 skill 绝对根路径的前提下表达 bundle 归属与 skill 同步状态；
- 运行时可以通过 `SKILL_STORAGE_ROOT + skill_dir_name` 解析 skill 本地目录。

## Phase 2：注册 API 变更

### 目标

让 bundle 注册接口能够描述 skill 内容来自哪个 GitHub URL。

### 任务

- [x] 扩展管理端请求体，支持：
  - `githubUrl`
- [x] 校验来源是否为受支持的 public GitHub URL
- [x] 保持当前 bundle 元数据行为不变
- [x] 为 GitHub URL 不合法或同步失败定义错误响应

### 完成标准

- 一次 bundle 注册请求可以完整描述 skill 元数据和 GitHub 内容来源；
- 非法 GitHub URL 会在同步开始前被拒绝。

## Phase 3：Skill 目录名规范化

### 目标

把逻辑上的 skill 名称映射为稳定且路径安全的目录名。

### 任务

- [x] 实现规范化：
  - 去掉首尾空白
  - 将空格转换为 `_`
  - 替换或移除不安全路径字符
- [x] 定义确定性的重名处理策略
- [x] 持久化最终 `skill_dir_name`
- [x] 后续所有文件系统访问都使用已持久化的目录名

### 完成标准

- 重复注册/更新不会导致不稳定的目录名漂移；
- 不同 bundle 下相同或冲突的 skill 名可以安全共存。

## Phase 4：注册时 Git 同步

### 目标

在注册/更新时将 skill 内容物化到磁盘。

### 任务

- [x] 实现 public GitHub 拉取的同步服务
- [x] 将 `githubUrl` 解析为 repo/ref/subpath，并且只拉取该目标，而不是保留完整仓库 checkout
- [x] 优先使用 archive/export 快照方式，或在必要时采用浅拉取 + 稀疏拉取
- [x] 将内容写入 `SKILL_STORAGE_ROOT` 下的临时目录
- [x] 将临时目录原子替换到最终 skill 目录
- [x] 从发布后的 skill 目录中移除 `.git` 元数据
- [x] 更新 `sync_status`：
  - 同步前为 `pending`
  - 成功后为 `ready`
  - 初次同步失败时为 `sync_failed`；如果已存在 `ready` 快照，则保持 `ready` 并记录 `sync_error`
- [x] 如果刷新同步失败，保留之前已 `ready` 的内容
- [x] 在成功或失败后清理临时目录
- [x] 默认只保留当前 `ready` 快照

### 完成标准

- 注册/更新成功时能产出一个可用的本地 skill 目录；
- 同步失败不会让 MCP 看到部分发布的内容。

## Phase 5：Bundle 级 MCP 面

### 目标

通过 MCP 暴露已注册的 skill 及其文件。

### 任务

- [x] 实现 bundle 级 `initialize`
- [x] 实现基于 PostgreSQL skill 记录的 `tools/list`
- [x] 实现基于已知 skill 目录文件枚举的 `resources/list`
- [x] 实现基于解析后的 skill 目录进行读取的 `resources/read`
- [x] 文本资源返回 `text`，二进制资源返回 `blob`
- [x] `mimeType` 通过扩展名或内容探测推断

### 完成标准

- 客户端连接某个 bundle 时，只能看到该 bundle 的 tools 和文件资源；
- 文件读取按原始内容类型返回，不做业务语义解析。

## Phase 6：安全与加固

### 目标

保护文件系统，并让同步生命周期在运维上可控。

### 任务

- [x] 防止 `..` 路径穿越和绝对路径逃逸
- [x] 拒绝跳出计算后 skill 根目录的符号链接
- [x] 默认忽略 `.git`、隐藏文件和临时产物
- [x] 让同步失败可以通过管理响应和持久化同步状态被观测到
- [x] 确保 inactive 或 non-ready 的 skill 永远不会被暴露
- [x] 将内容去重定义为后续可选优化，而不是 MVP 依赖

### 完成标准

- MCP 文件访问被严格限制在计算得到的 skill 目录内；
- 面对非法输入和部分失败场景时，同步与暴露行为仍然安全。

## Phase 7：测试

### 目标

覆盖关键正确性与安全路径。

### 任务

- [x] 增加空格、不安全字符和重名冲突的规范化测试
- [x] 增加非法 GitHub URL 的注册测试
- [x] 增加同步成功、失败和重试场景测试
- [x] 增加 MCP 测试，覆盖：
  - bundle 作用域隔离
  - `tools/list`
  - `resources/list`
  - `resources/read`
  - 文本与二进制返回路径
- [x] 增加路径穿越与符号链接逃逸测试

### 完成标准

- Git 同步与资源读取这条核心链路有自动化测试覆盖。

## 建议实施顺序

1. schema / 模型更新
2. 名称规范化
3. 注册 API 扩展
4. Git 同步服务
5. MCP handlers
6. 安全加固
7. 测试

这样安排可以减少来回返工，因为 MCP handlers 依赖注册与同步模型先稳定下来。

## MVP 范围外

- `tools/call`
- prompt 特定语义
- skill 类型特定解析
- private GitHub 鉴权
- 运行时磁盘发现
- bundle 级文件系统布局
- SSE / subscribe 支持

## 参考文档

- API 与协议设计：`docs/bundle-mcp-api-design.md`
