# Bundle MCP API Design

## Goal

Implement a **bundle-scoped MCP server** where:

- each bundle is exposed externally as its own MCP server;
- bundle metadata lives in PostgreSQL;
- each registered skill is synced from a **public GitHub URL** to a local filesystem directory;
- MCP exposes registered skill files as resources without interpreting their business meaning.

## Architecture Summary

### Bundle

- bundle metadata is stored in PostgreSQL only;
- bundle does **not** have a dedicated directory on disk;
- bundle remains the external MCP server boundary.

### Skill

- each skill belongs to a bundle in PostgreSQL;
- each skill is materialized to a local filesystem directory during registration or update;
- runtime never scans disk to discover skills;
- runtime uses PostgreSQL as the source of truth for skill existence and ownership.

### Resource Model

- `tool = skill`
- `resource = file inside a registered skill directory`

Example resource URI:

```text
skillfun://skills/{skillName}/files/{relativePath}
```

## Storage Model

### PostgreSQL

Bundle records continue to live in `bundles`.

Skill records need additional sync-oriented metadata beyond the current `nft_id`, `tool_name`, and `schema_json` fields.

The external API should accept a single `githubUrl`, while the backend may persist normalized fields parsed from that URL.

Recommended new fields:

| Field | Purpose |
| --- | --- |
| `bundle_name` or relation via `bundle_skills` | bundle ownership |
| `tool_name` | MCP tool / skill name |
| `skill_dir_name` | normalized path-safe directory name |
| `github_url` | original public GitHub URL from the admin API |
| `github_repo` | normalized `owner/repo` parsed from the URL |
| `github_ref` | normalized branch, tag, or commit parsed from the URL |
| `github_subpath` | normalized subdirectory path parsed from the URL |
| `sync_status` | `pending`, `ready`, `failed` |
| `last_synced_at` | last successful sync time |
| `sync_error` | latest sync failure detail for ops/admin |

### Filesystem

The filesystem stores only skill content directories.

The project uses one shared environment variable as the storage root, for example:

```text
SKILL_STORAGE_ROOT=/var/lib/skillfun/skills
```

Recommended root:

```text
${SKILL_STORAGE_ROOT}/{skill_dir_name}/
```

There is intentionally **no** bundle directory level.

## Skill Directory Name Rules

The skill directory name is derived from the skill name and persisted in PostgreSQL.

Normalization rules:

1. trim leading and trailing whitespace;
2. convert spaces to `_`;
3. replace or remove characters that are unsafe in paths;
4. store the resulting value as `skill_dir_name`.

Examples:

| Skill name | Directory name |
| --- | --- |
| `Current Weather` | `Current_Weather` |
| `foo/bar` | `foo_bar` |
| `a:b*c` | `a_b_c` |

Collision handling:

1. try the normalized base name first;
2. if already used, append a stable suffix;
3. persist the final chosen name in PostgreSQL.

Recommended suffix strategy:

```text
{normalized}__{skill_id}
```

This avoids directory name drift across retries.

## Registration and Sync Workflow

Skill registration is not just a DB write. It must include Git sync.

### Registration flow

1. validate bundle exists and can be updated;
2. validate the skill payload;
3. normalize `skill_name` into `skill_dir_name`;
4. resolve naming collisions;
5. insert or update the skill row with `sync_status = pending`;
6. parse the GitHub URL and fetch the requested repo/ref/subpath;
7. materialize content into a temporary local directory;
8. atomically swap the temp directory into the final computed skill path;
9. update the skill row with:
   - `sync_status = ready`
   - `last_synced_at`
10. expose the skill through MCP only when status is `ready`.

### Failure handling

- Git fetch failure keeps the skill out of MCP exposure;
- failed sync leaves `sync_status = failed`;
- partial temp directories must be cleaned up;
- previously ready content should remain intact until the new sync succeeds.

### Disk usage optimization

The recommended storage strategy is to keep only the **final skill snapshot**, not a full persistent Git checkout.

Recommended rules:

1. fetch only the requested subpath parsed from `githubUrl`, not the whole repository working tree;
2. prefer archive/export style snapshot fetches when possible;
3. if Git clone is required, prefer shallow and sparse fetches such as:
   - `--depth=1`
   - `--filter=blob:none`
   - `--sparse`
4. remove `.git` metadata from the published skill directory;
5. keep only the current `ready` version on disk after a successful swap;
6. clean up temp directories and downloaded archives immediately after publish;
7. optionally deduplicate identical `repo + commit + subpath` snapshots across skills later.

This keeps runtime storage focused on MCP-readable files rather than Git history.

## Runtime Lookup Model

### No disk-based discovery

The server must not crawl disk to discover bundles or skills.

Runtime discovery source:

- bundles: PostgreSQL
- skills: PostgreSQL
- resource file enumeration for a known skill: the computed path `${SKILL_STORAGE_ROOT}/{skill_dir_name}`

### Runtime access pattern

1. resolve the bundle from host or path;
2. query PostgreSQL for active skills in that bundle;
3. when needed, read files only inside the matched skill's computed storage path.

## MCP Surface

## External endpoints

Recommended canonical endpoint:

```text
https://{bundle}.skillfun.ai/mcp
```

Fallback/internal endpoint:

```text
https://gateway.skillfun.ai/{bundle}/mcp
```

Transport:

- Streamable HTTP
- `POST` required
- `GET` optional and can return `405` for MVP
- `DELETE` optional

## Supported MCP methods

MVP methods:

1. `initialize`
2. `tools/list`
3. `resources/list`
4. `resources/read`

Not in MVP:

- `tools/call`
- `resources/subscribe`
- `resources/templates/list`
- `prompts/*`

## Method semantics

### `tools/list`

Returns all active skills for the current bundle from PostgreSQL.

Each skill is one tool:

- `name`
- `description`
- `inputSchema`

No runtime disk discovery is involved.

### `resources/list`

Returns file resources across the current bundle's registered skills.

For each skill:

1. load `skill_dir_name` from PostgreSQL;
2. compute the skill path from `SKILL_STORAGE_ROOT + skill_dir_name`;
3. enumerate files inside that directory;
4. convert each file into an MCP resource entry.

Each file becomes one resource.

Example:

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

Reads a file under a registered skill directory.

Flow:

1. parse `skillName` and `relativePath` from the resource URI;
2. resolve the skill row inside the current bundle from PostgreSQL;
3. load `skill_dir_name`;
4. compute the skill path from `SKILL_STORAGE_ROOT + skill_dir_name`;
5. validate the requested path stays inside the skill root;
6. read the file;
7. return the original type:
   - text files as `text`
   - binary files as `blob`
   - always include `mimeType`

Text example:

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

Binary example:

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

## Admin API Proposal

The current bundle admin endpoints already exist:

- `POST /v1/mcp/bundles`
- `PUT /v1/mcp/bundles/:bundleName`
- `DELETE /v1/mcp/bundles/:bundleName`

The skill payload should evolve from inline content to a GitHub-backed sync source.

### Create or update bundle

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

### Proposed skill request fields

| Field | Required | Notes |
| --- | --- | --- |
| `nftId` | yes | skill ownership reference |
| `name` | yes | logical skill/tool name |
| `description` | yes | tool summary |
| `inputSchema` | yes | MCP tool input schema |
| `githubUrl` | yes | public GitHub URL pointing at the skill directory or snapshot root |

### Proposed bundle response extension

The success response can keep the existing `bundle` object and may later add skill sync status if needed.

Example:

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

If desired later, an admin-only detail endpoint can expose:

- `skillDirName`
- `skillRootPath`
- `syncStatus`
- `lastSyncedAt`
- `syncError`

## Validation Rules

### Bundle-level

- `bundleName` required;
- `displayName` required;
- `subdomain` must follow existing repository rules;
- only active bundles are externally exposed.

### Skill-level

- `name` required;
- `description` required;
- `inputSchema` must be valid JSON;
- `githubUrl` must be a supported public GitHub URL;
- `githubUrl` must parse into repo, ref, and effective subpath;
- `skill_dir_name` derived from `name` using path-safe normalization;
- storage root comes from a shared environment variable, not the database;
- collisions must be deduplicated deterministically.

## Security Constraints

### GitHub source constraints

- only public GitHub sources are supported in MVP;
- restrict allowed URL schemes;
- reject local paths and unsafe transports;
- treat Git content as untrusted input.

### Storage optimization constraints

- the published skill directory should contain only the final resource snapshot;
- `.git` metadata must not remain in the published directory;
- the system should not retain old inactive snapshots by default;
- temporary fetch and extraction artifacts must be deleted after sync.

### Filesystem constraints

- forbid `..` traversal;
- forbid absolute paths from resource requests;
- resolve and reject symlink escapes;
- expose only files under `${SKILL_STORAGE_ROOT}/{skill_dir_name}`;
- ignore `.git`, hidden files, and temp artifacts by default.

## Error Model

### Admin API

- invalid payload → `400`
- duplicate/conflicting naming outcome that cannot be resolved → `409`
- Git sync failure during registration/update → `500` or explicit sync failure status
- unknown bundle → `404`

### MCP

- unknown method → JSON-RPC `-32601`
- invalid params → JSON-RPC `-32602`
- missing skill or file → application not found error
- file outside the skill root → forbidden / invalid params

## Task Breakdown

### Phase 1: schema and models

1. extend skill storage metadata for Git-backed sync;
2. persist `skill_dir_name`;
3. add sync status fields.

### Phase 2: registration-time sync

1. extend bundle admin request payloads;
2. validate the `githubUrl` field and its parsed repo/ref/subpath;
3. implement name normalization and collision handling;
4. implement Git fetch + temp-dir swap;
5. mark skills `ready` only after successful sync.

### Phase 3: MCP read surface

1. implement bundle-scoped `initialize`;
2. implement DB-backed `tools/list`;
3. implement file-backed `resources/list`;
4. implement file-backed `resources/read`.

### Phase 4: hardening

1. path traversal and symlink protections;
2. hidden file filtering;
3. structured sync error reporting;
4. tests for collisions, failed sync, and resource access rules.

## Notes

- This document intentionally describes the target API and storage model, not just the current implementation.
- It aligns with the latest product decision that bundle identity is database-backed while skill content is Git-synced and file-based.
