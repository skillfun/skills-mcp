# Bundle MCP Implementation Plan

## Goal

Implement a bundle-scoped MCP server where:

- bundle metadata and routing stay in PostgreSQL;
- each registered skill is synced from a public GitHub URL to local disk during registration;
- the MCP surface exposes registered skill files as resources;
- runtime does not discover skills by scanning disk.

This plan is the execution companion to `docs/bundle-mcp-api-design.md`.

## Locked Decisions

### Bundle boundary

- each bundle is exposed externally as its own MCP server;
- bundle does not have a dedicated directory on disk;
- bundle metadata remains in PostgreSQL.

### Skill storage

- each skill maps to one local directory;
- the directory name is derived from the skill name;
- spaces become underscores;
- only path-safe characters are allowed;
- collisions must be deduplicated and the final directory name persisted.

### Storage root

- the project uses one shared environment variable as the root directory for all synced skills;
- example:

```text
SKILL_STORAGE_ROOT=/var/lib/skillfun/skills
```

- the effective skill path is computed as:

```text
${SKILL_STORAGE_ROOT}/{skill_dir_name}
```

### Sync source

- skill content is synced at registration/update time;
- current MVP only supports public GitHub URLs;
- runtime MCP reads from the already-synced local directory.
- the published directory should be a final snapshot, not a persistent full Git checkout;
- `.git` metadata must be removed from the published directory;
- by default, only the current `ready` version should remain on disk after publish.

## Deliverables

- [x] schema updates for GitHub-backed skill sync
- [x] registration API extension for GitHub source metadata
- [x] skill name normalization and collision handling
- [x] registration-time Git sync workflow
- [x] bundle-scoped MCP `initialize`, `tools/list`, `resources/list`, and `resources/read`
- [x] validation and tests for path safety, failed sync, and name collisions

## Work Breakdown

## Phase 1: Schema and model updates

### Goal

Make PostgreSQL the source of truth for bundle ownership, skill registration, and sync state.

### Tasks

- [x] extend the skill model with GitHub sync metadata:
  - `github_url`
  - optional parsed `github_repo`
  - optional parsed `github_ref`
  - optional parsed `github_subpath`
  - `skill_dir_name`
  - `sync_status`
  - `last_synced_at`
  - `sync_error`
- [x] decide whether these fields live directly on `skills` or in a related table
- [x] add/update queries and store methods
- [x] ensure only `ready` skills are exposed through MCP

### Done when

- bundle ownership and skill sync state can be represented without storing per-skill absolute root paths;
- runtime can resolve a skill's local directory from `SKILL_STORAGE_ROOT + skill_dir_name`.

## Phase 2: Registration API changes

### Goal

Let bundle registration describe where skill content comes from on GitHub.

### Tasks

- [x] extend admin request payloads to accept:
  - `githubUrl`
- [x] validate that the source is a supported public GitHub URL
- [x] preserve current bundle metadata behavior
- [x] define error responses for invalid GitHub URL input or sync failures

### Done when

- a bundle registration request can fully describe both skill metadata and the GitHub content source;
- invalid GitHub URL input is rejected before sync starts.

## Phase 3: Skill directory name normalization

### Goal

Map each logical skill name to a stable path-safe directory name.

### Tasks

- [x] implement normalization:
  - trim surrounding whitespace
  - convert spaces to `_`
  - replace or remove unsafe path characters
- [x] define deterministic collision handling
- [x] persist the final `skill_dir_name`
- [x] use the persisted name for all later filesystem access

### Done when

- repeated registration/update does not produce unstable directory drift;
- same or colliding names across bundles can coexist safely.

## Phase 4: Registration-time Git sync

### Goal

Materialize skill content onto disk during registration/update.

### Tasks

- [x] create a sync service for public GitHub fetch
- [x] parse `githubUrl` into repo/ref/subpath and fetch only that target, not a full persistent repository checkout
- [x] prefer archive/export snapshot fetches or shallow+sparse Git fetches where needed
- [x] write content into a temporary directory under `SKILL_STORAGE_ROOT`
- [x] atomically swap temp content into the final skill directory
- [x] remove `.git` metadata from the published skill directory
- [x] update `sync_status` to:
  - `pending` before sync
  - `ready` on success
  - `sync_failed` on initial sync error, while previously `ready` snapshots keep `ready` and record `sync_error`
- [x] retain previously ready content if a refresh sync fails
- [x] clean up temp directories after success or failure
- [x] retain only the current `ready` snapshot by default

### Done when

- registration/update produces a ready local skill directory on success;
- failed syncs do not leave partially published content visible to MCP.

## Phase 5: Bundle-scoped MCP surface

### Goal

Expose registered skills and their files through MCP.

### Tasks

- [x] implement bundle-scoped `initialize`
- [x] implement `tools/list` from PostgreSQL skill records
- [x] implement `resources/list` by enumerating files under known skill directories
- [x] implement `resources/read` by reading files under the resolved skill directory
- [x] return text resources as `text` and binary resources as `blob`
- [x] set `mimeType` from extension or content sniffing

### Done when

- a client connected to one bundle sees only that bundle's tools and file resources;
- file reads return the original content type without business-specific parsing.

## Phase 6: Safety and hardening

### Goal

Protect the filesystem and make the sync lifecycle operationally safe.

### Tasks

- [x] prevent `..` traversal and absolute-path escapes
- [x] reject symlink escapes outside the computed skill root
- [x] ignore `.git`, hidden files, and temp artifacts by default
- [x] make sync failures observable through admin responses and stored sync state
- [x] ensure inactive or non-ready skills are never exposed
- [x] define later content deduplication as an optional optimization, not an MVP dependency

### Done when

- MCP file access is constrained to the computed skill directory;
- sync and exposure behavior remain safe under malformed inputs and partial failures.

## Phase 7: Tests

### Goal

Cover the critical correctness and safety paths.

### Tasks

- [x] add normalization tests for spaces, unsafe characters, and collisions
- [x] add registration tests for invalid GitHub URL input
- [x] add sync workflow tests for success, failure, and retry cases
- [x] add MCP tests for:
  - bundle scoping
  - `tools/list`
  - `resources/list`
  - `resources/read`
  - text vs binary return paths
- [x] add path traversal and symlink escape tests

### Done when

- the core Git-sync and resource-read lifecycle is covered by automated tests.

## Suggested Implementation Order

1. schema/model updates
2. name normalization
3. registration API extension
4. Git sync service
5. MCP handlers
6. hardening
7. tests

This order reduces thrash because MCP handlers depend on the registration and sync shape being stable first.

## Out of Scope for MVP

- `tools/call`
- prompt-specific semantics
- skill-type-specific parsing
- private GitHub authentication
- runtime disk discovery
- bundle-level filesystem layout
- SSE / subscribe support

## References

- API and protocol design: `docs/bundle-mcp-api-design.md`
