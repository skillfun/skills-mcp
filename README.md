# SkillFun MCP Gateway

SkillFun MCP Gateway 是一个基于 Gin 的 MCP 路由网关：

- PostgreSQL 保存 bundle、skill、同步状态和支付授权信息
- skill 在注册/更新时从 public GitHub URL 同步到本地磁盘
- 运行时按 bundle 暴露 `tools/list`、`resources/list`、`resources/read`
- 对外域名由外部共享 Caddy 处理，应用自身只依赖 `/{bundle}/mcp` 和 `/{bundle}/tools`

## 部署形态

当前仓库按下面的方式部署：

1. `gateway` 容器监听 `5080`
2. `postgres` 容器保存元数据
3. `skill_storage` 卷保存同步后的 skill 文件
4. 机器上的共享 Caddy 负责 TLS 和 bundle 子域名到路径路由的改写

推荐入口：

- `https://{bundle}.mcp.example.com/mcp`
- `https://{bundle}.mcp.example.com/tools`

外部 Caddy 会把它们改写为：

- `http://gateway:5080/{bundle}/mcp`
- `http://gateway:5080/{bundle}/tools`

同时保留一个管理/兜底入口：

- `https://gateway.example.com/v1/mcp/bundles`
- `https://gateway.example.com/{bundle}/mcp`

## `.env` 配置

`docker-compose.yml` 直接读取仓库根目录的 `.env` 做变量替换。

推荐的 `.env`：

```dotenv
GATEWAY_IMAGE=ghcr.io/OWNER/skills-mcp:latest
POSTGRES_DB=skillfun
POSTGRES_USER=skillfun
POSTGRES_PASSWORD=change-me
BUNDLE_ADMIN_TOKEN=change-me
SKILL_STORAGE_ROOT=/var/lib/skillfun/skills
```

## 必需环境变量

| 变量 | 用途 |
| --- | --- |
| `GATEWAY_IMAGE` | `docker-compose.yml` 使用的网关镜像，例如 `ghcr.io/OWNER/skills-mcp:latest` |
| `POSTGRES_DB` | PostgreSQL 数据库名 |
| `POSTGRES_USER` | PostgreSQL 用户名 |
| `POSTGRES_PASSWORD` | PostgreSQL 密码 |
| `BUNDLE_ADMIN_TOKEN` | 管理 API 的 Bearer Token |
| `SKILL_STORAGE_ROOT` | skill 本地存储目录，默认建议 `/var/lib/skillfun/skills` |

网关进程自身依赖的核心环境变量是：

- `DATABASE_URL`
- `SKILL_STORAGE_ROOT`
- `BUNDLE_ADMIN_TOKEN`

在 Compose 中会自动生成：

- `DATABASE_URL=postgres://...@postgres:5432/...?...`
- `GIN_MODE=release`

## 构建并发布镜像

仓库提供了 `.github/workflows/build-image.yml`：

- `pull_request`：仅构建镜像，验证 Dockerfile 可用
- `push` 到 `main`：构建并推送 `latest`、分支名和 `sha-*` 标签
- `push` tag `v*`：额外推送 tag 同名标签
- `workflow_dispatch`：手动触发构建和推送

镜像默认发布到：

```text
ghcr.io/<owner>/<repo>
```

例如当前仓库会发布成：

```text
ghcr.io/<owner>/skills-mcp
```

如果 GHCR 包是私有的，部署机需要先登录：

```bash
docker login ghcr.io
```

## 使用 Docker Compose 部署

1. 在仓库根目录创建 `.env`。

2. 启动服务：

```bash
docker compose pull
docker compose up -d
```

3. 查看状态：

```bash
docker compose ps
docker compose logs -f gateway postgres
```

4. 本机直连调试网关：

```bash
curl http://127.0.0.1:5080/<bundle>/tools
```

## 外部 Caddy 与 DNS 说明

- 共享 Caddy 所在机器需要能访问 `127.0.0.1:5080`
- `gateway.example.com` 需要解析到部署机，用作管理/兜底入口
- `*.mcp.example.com` 需要 wildcard DNS 指向部署机，用作 bundle 子域名入口

推荐的 Caddy 配置可以直接写到你现有的共享 Caddy 中：

```caddy
gateway.example.com {
    reverse_proxy 127.0.0.1:5080
}

*.mcp.example.com {
    @bundle host_regexp bundle ^([a-z0-9-]+)\.mcp\.example\.com$
    rewrite @bundle /{re.bundle.1}{uri}
    reverse_proxy 127.0.0.1:5080
}
```

把其中的：

- `gateway.example.com`
- `mcp.example.com`

替换成你的真实域名即可。

如果你要在公网直接启用 `*.mcp.example.com` 的 HTTPS，通常还需要：

1. wildcard DNS
2. 共享 Caddy 支持 DNS challenge
3. 对应 DNS provider 的凭证配置

如果暂时不做 wildcard 证书，也可以先只用：

- `https://gateway.example.com/{bundle}/mcp`

应用本身不依赖任何固定外部域名。

## 管理 API

管理接口由 `BUNDLE_ADMIN_TOKEN` 保护：

- `POST /v1/mcp/bundles`
- `PUT /v1/mcp/bundles/:bundleName`
- `DELETE /v1/mcp/bundles/:bundleName`

示例：

```bash
curl --request POST \
  --url "https://gateway.example.com/v1/mcp/bundles" \
  --header "Authorization: Bearer ${BUNDLE_ADMIN_TOKEN}" \
  --header "Content-Type: application/json" \
  --data '{
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
  }'
```

## 开发验证

```bash
go test ./...
docker compose config
```
