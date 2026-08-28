# OrbitCloud

> English | [简体中文](docs/README.zh.md)

OrbitCloud is a **Go + Vue3** distributed cloud drive: metadata is managed by GORM (SQLite for dev / PostgreSQL for production, with read-write splitting), file data is written to object storage via the **S3 protocol** (compatible with RustFS / MinIO, etc.). It supports users & permissions, user groups, buckets, a directory tree, file management, sharing, batch operations, in-browser preview, and an **SMB gateway** (mount directly in Windows Explorer).

## Features

- **Accounts & Permissions**: JWT dual-token with refresh-token rotation; registration / user management; tiered permission model (0 super admin / 1 admin / 2 reserved / 3 normal user, extensible); user groups + item-level visibility ACL
- **Storage & Files**: bucket management; directory tree (mkdir -p auto-creates parent chain); upload / batch upload / streaming download / online preview / rename / move / copy / delete; background tasks for cascading folder delete & copy (crash-resumable); batch operations (≤100 items, processed independently); bucket quota; dual object-storage drivers (`s3` / `local`)
- **Sharing**: time-limited / count-limited / extraction-code shares, for files and folders
- **SMB Access**: Rust SMB gateway (445) over a private frame protocol; share name = bucket name; mountable via Windows Explorer / `net use` / Linux cifs
- **Platform**: config-driven (embedded default config); GORM AutoMigrate; read-write splitting (dbresolver); log rotation with optional DB persistence; scheduled cleanup tasks; global goroutine pool; graceful shutdown; CLI commands (`-initConfig` / `--add-superadmin` / `--version` / `--help`)

## Tech Stack

| Layer | Choice |
|---|---|
| Language | Go 1.25+ / Rust 1.95+ (SMB gateway) |
| HTTP | gin |
| JWT | golang-jwt/jwt/v5 |
| ORM | gorm + dbresolver |
| Database | SQLite (dev) / PostgreSQL (prod) |
| Object storage | minio-go/v7 (S3) or local directory driver |
| SMB | ixr-smb-server (Rust) |
| Frontend | Vue 3 + Vite + TS + Element Plus + Pinia |

## Quick Start (Recommended: Docker Compose with Prebuilt Binaries)

> The repo ships containerized deployment based on **prebuilt binaries** — no source build required; the full stack (backend + SMB gateway + RustFS + frontend) is up in minutes.

```powershell
# 1. Prepare: copy binaries into build contexts + generate random-key configs (see docker/README.md)
powershell -ExecutionPolicy Bypass -File docker\prep.ps1

# 2. Build and start
docker compose -f docker\docker-compose.yml up -d --build

# 3. Create the super admin (first run only)
docker compose -f docker\docker-compose.yml exec orbitcloud /usr/local/bin/orbitcloud --add-superadmin admin <password≥8>

# 4. Access
#    Web   : http://127.0.0.1:18080
#    API   : http://127.0.0.1:8080/api/v1
#    SMB   : \\127.0.0.1\<bucket-name>(port from docker\.env PORT_SMB, default 1445)
```

See [docker/README.md](docker/README.md) for architecture, keys, volumes, ops commands and known constraints.

## Deployment Options

| Option | Description | Best for |
|---|---|---|
| **Docker Compose (binaries)** | No build; 4 containers, one command | Quick trial / private delivery |
| systemd manual deploy | Traditional Linux deployment (backend + RustFS + SMB + Nginx) | Single production server |
| Deploy Tool | Single-window desktop: audit → precheck → generate deterministic deploy scripts | Operations staff |

### systemd (manual)

```bash
# Prepare Go ≥1.25 toolchain on the server, or use prebuilt dist/ binaries directly:
sudo bash deploy/deploy.sh --superadmin admin <password>
# Frontend (needs Nginx + Node): deploy/deploy.sh --only-frontend
```

### Deploy Tool (deploy-tool)

`OrbitCloudDeployTool.exe` is a standalone single-window desktop tool (Go + WebView2, ~12MB, no install):

- **Audit** (read-only): environment / services / ports / cross-side config consistency (shared_key, jwt.secret, log level, storage driver); auto-produces warnings and suggestions (checkable as todos)
- **Precheck**: disk / toolchains / directories / health as PASS/WARN; **LSP-style live precheck** — edits debounce-triggered checks automatically
- **Script generation**: parameterized form (dropdowns/inputs/toggles) → validation of inappropriate values (error blocks / warn hints) → deterministic `.sh` output (keys auto-generated and embedded; fully previewable / copyable / saveable)
- **Native SSH**: built-in SSH channel (credentials compatible with sshbox.json), no external programs; deploy scripts embedded in the binary — fully self-contained

See [deploy-tool/README.md](deploy-tool/README.md).

## Documentation

| Doc | Content |
|---|---|
| [Deployment Guide](docs/部署指南.md) | Quick start / production deploy (Linux systemd, Windows NSSM, Docker) / object storage / Nginx / ops |
| [Deployment & Windows Mount Manual](docs/部署与Windows挂载手册.md) | One-click server deploy + Windows Explorer SMB mount walkthrough |
| [Configuration Reference](docs/配置参考.md) | Full `OrbitCloud.yaml` fields, env-var overrides, minimal production changes |
| [API Reference](docs/API参考.md) | All endpoints, request/response examples, error codes, auth notes |
| [Architecture](docs/架构设计.md) | Layers, data model, permission model, storage design, key flows |
| [Build & Release](docs/构建与发布.md) | Cross-compile scripts, packaging, versioning |
| [Development Guide](docs/开发指南.md) | Environment setup, layering rules, adding an API, testing & commit conventions |

## Build (Three Platforms in One Command)

| Platform | Script | Output |
|---|---|---|
| Linux / macOS / WSL | `bash build/build.sh --all --frontend` | `build/dist/` |
| Any (Git Bash) | See [Build & Release](docs/构建与发布.md) | — |

Or use the **prebuilt artifacts** in `dist/` at the repo root: three-platform backend, two-platform SMB gateway, frontend `web/`, with `sha256sums.txt` checksums.

```bash
# Build backend + SMB gateway + frontend for the current platform (needs Go/Rust/Node)
./build/build.sh --all --frontend
```

## CLI Commands

### Backend (`orbitcloud`)

| Command | Description |
|---|---|
| `orbitcloud` | Start the service (reads `./OrbitCloud.yaml`; auto-generates from embedded defaults if missing) |
| `orbitcloud -initConfig` | Force-generate `./OrbitCloud.yaml` from embedded defaults and exit |
| `orbitcloud --add-superadmin <username> <password>` | Create level-0 super admin and exit (only way; password ≥8 chars; not idempotent) |
| `orbitcloud --version` | Print version and exit |
| `orbitcloud --help` / `-h` | Print help and exit |

### SMB Gateway (`smb-gateway`)

| Command | Description |
|---|---|
| `smb-gateway` | Start the gateway (reads `./SMB.yaml`; auto-generates from embedded defaults if missing) |
| `smb-gateway -initConfig` | Force-generate `./SMB.yaml` from embedded defaults and exit |
| `smb-gateway --version` | Print version and exit |
| `smb-gateway --help` / `-h` | Print help and exit |
| `smb-gateway --WithConfig <json>` | Start with an injected JSON config (overrides `SMB.yaml`) |

Shared key: injected via the `ORBITCLOUD_SMB_GATEWAY_KEY` environment variable (≥16 bytes; must match the backend's `smb_gateway.shared_key`).

## Configuration

Both components are config-driven with embedded defaults — no fallback values: missing/invalid config aborts startup.

| Component | File (working dir) | Key sections | Reference |
|---|---|---|---|
| Backend | `./OrbitCloud.yaml` | `app` `server` `jwt` `storage` `health` `pool` `database` `nodes` `bucket` `smb_gateway` `log` | [Configuration Reference](docs/配置参考.md) |
| SMB gateway | `./SMB.yaml` | `smb` (listen/netbios) `gateway` (addr/shared_key/heartbeat) `log` | [deploy-smb-gateway.sh](deploy/deploy-smb-gateway.sh) |

Sensitive values (JWT secret, SMB shared key, S3 credentials) should be injected via environment variables — see [Configuration Reference](docs/配置参考.md).

## Dependencies

### Go (backend)

| Dependency | Purpose |
|---|---|
| gin | HTTP framework |
| gorm + dbresolver | ORM with read-write splitting |
| golang-jwt/jwt/v5 | JWT access/refresh tokens |
| minio-go/v7 | S3 object storage client |
| glebarez/sqlite + modernc | Pure-Go SQLite driver (CGO-free) |
| gorm.io/driver/postgres | PostgreSQL driver |
| Yiming1997/agilePool | Global goroutine pool |
| gopkg.in/yaml.v3 | Config parsing |

### Rust (SMB gateway)

| Dependency | Purpose |
|---|---|
| tokio | Async runtime |
| ixr-smb-server (patched, vendored) | SMB protocol server |
| rusqlite (bundled) | Local log DB |
| serde / serde_yaml | Config & serialization |
| hmac / sha2 | Handshake auth |
| tracing / tracing-subscriber | Logging |

### Frontend (Vue 3)

| Dependency | Purpose |
|---|---|
| Vue 3 + vue-router + Pinia | Core / routing / state |
| Element Plus + icons | UI library |
| axios | HTTP client |
| fflate | Client-side zip (batch download) |
| Vite + TypeScript + vue-tsc | Build & type checking |

## Directory Layout

```
├── main.go                 # Entry: config → init → HTTP → background tasks → graceful shutdown
├── config/                 # Config structs + embedded defaults (OrbitCloud.yaml)
├── appinit/                # Initializers (config/log/DB/JWT/storage/pool)
├── core/                   # Global singletons + JWT service + object-storage abstraction
├── common/                 # Sentinel errors / unified response / paging / validation
├── model/                  # GORM models
├── server/                 # Business layer (package-level functions)
├── api/                    # HTTP layer (routes/handlers/middleware)
├── cron/                   # Scheduled tasks
├── flag/                   # CLI commands
├── log/                    # Log wrapper (rotation, optional DB persistence)
├── utils/                  # Bucket-name encoding / sampled MD5 / range parsing
├── smb_server/             # SMB gateway (go/ embedded component + rust/ standalone gateway)
├── frontend/               # Vue3 frontend
├── build/                  # Build scripts (cross-compilation)
├── deploy/                 # Deploy scripts (systemd)
├── docker/                 # Docker Compose deployment with prebuilt binaries (incl. README)
├── deploy-tool/            # Deploy ops tool (single-window desktop, Go + WebView2)
├── dist/                   # Prebuilt artifacts (3-platform backend / gateways + frontend web/)
├── docs/                   # Documentation
└── tem/                    # Temporary research & external sources (e.g. sshbox)
```

## Layering Convention

```
main(startup/shutdown) → appinit(init) → core(global singletons)
api(HTTP layer: auth/validation/error mapping)
server(business layer: package-level functions)
model(GORM models)  common(sentinel errors/paging/validation)  utils(bucket encoding/sampled MD5)
```

- `core` is the lowest-level infrastructure: only global singletons, never imports business packages
- `server` exposes package-level functions that access `core` singletons directly
- `api` is stateless: parses params, authenticates, calls server, maps sentinel errors to HTTP codes and the unified body `{code, data, message}`
- Object keys: file record primary ID; bucket name = `utils.BucketEncoder(bucketID)` (base-36), independent of file names/paths

## API Overview

Unified prefix `/api/v1`; success: `{code:0, data:{...}, message:"ok"}`, error: `{code:-1, message:"..."}`.

### Auth & Users

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/auth/login` | public | Login, returns access/refresh tokens + user |
| POST | `/auth/refresh` | public | Refresh token (rotating: old token invalidated) |
| POST | `/auth/register` | admin | Register user (level 0 cannot be created via API) |
| POST | `/auth/logout` | logged-in | Revoke refresh token |
| GET/PUT | `/users/me` | logged-in | Current user info / change password·name·email |
| GET | `/users` | admin | Paginated user list |
| PUT/DELETE | `/users/:id` | admin | Modify others (level/status) / delete (soft) |

### User Groups

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/users/me/groups` | logged-in | My groups |
| POST/GET | `/groups`、`/groups/:id` | admin | Group CRUD |
| POST/DELETE | `/groups/:id/members`、`/groups/:id/members/:uid` | admin | Add / remove members |

### Buckets

| Method | Path | Auth | Description |
|---|---|---|---|
| POST/GET | `/buckets` | logged-in | Create / list |
| GET | `/buckets/:id` | logged-in | Bucket detail (quota/used) |
| PUT/DELETE | `/buckets/:id` | logged-in | Update / delete (cascading background cleanup) |

### Files

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/buckets/:id/files` | logged-in | Upload (multipart field `file`) |
| POST | `/buckets/:id/files/batch` | logged-in | Batch upload (field `files`) |
| GET | `/buckets/:id/files` | logged-in | List (paginated) |
| GET | `/buckets/:id/files/:fid` / `/dirs/:fid` | logged-in | File / folder metadata |
| GET | `/buckets/:id/files/:fid/download` | logged-in | Download (streaming) |
| GET | `/buckets/:id/files/:fid/preview` | logged-in | Online preview |
| POST | `/buckets/:id/files/:fid/copy` | logged-in | Copy file |
| POST | `/buckets/:id/files/:fid/move` | logged-in | Move/rename file |
| POST | `/buckets/:id/dirs/:fid/move` | logged-in | Move/rename folder |
| DELETE | `/buckets/:id/files/:fid` | logged-in | Delete file |
| POST | `/buckets/:id/dirs` | logged-in | Create folder (mkdir -p) |
| DELETE | `/buckets/:id/dirs/:fid` | logged-in | Delete folder (background DFS cleanup) |
| POST | `/buckets/:id/dirs/:fid/copy` | logged-in | Copy folder (background CopyTask) |
| POST | `/buckets/:id/items/batch-delete` / `batch-copy` / `batch-move` | logged-in | Batch operations (≤100) |
| PUT | `/buckets/:id/files/:fid/visibility` / `/dirs/:fid/visibility` | logged-in | Set item visibility groups |

### Shares

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/share/:token` | public | Resolve / download share (extraction code via query param) |
| POST/GET | `/shares` | logged-in | Create / list |
| PUT/DELETE | `/shares/:id` | logged-in | Update / delete (creator only) |

## Permission Model

- `PermissionLevel` — lower value = higher privilege; defined: 0 super admin, 1 admin, 2 reserved, 3 normal user; extensible within 0–9
- Admin check is uniformly `PermissionLevel.IsAdmin()` (0 / 1)
- Level 0 is only created via the CLI `--add-superadmin`; the HTTP API always normalizes to normal users
- Admins must hold strictly higher privilege than the target when modifying/deleting others
- Item-level group ACL is stackable (`VisibleToGroups` JSON list of group IDs; empty = unrestricted; non-empty = admins/group members only; uploader is not auto-granted)
- Bucket-level: access level and manage level are independent (manage defaults to access; manage must not be looser than access); `bucket.admin_create_only` restricts bucket creation to admins

## Background Tasks & Robustness

- Delete/copy tasks: mark target unavailable → persist task → background depth-first processing with crash-resume
- Optimistic lock prevents duplicate processing; hourly cleanup of stale logs/tokens/shares
- Upload compensation: remove record if object write fails; remove object if metadata update fails — no orphans
- 500s map to a unified "internal error"; invalid/missing config or unavailable storage terminates startup

## License

[Apache License 2.0](LICENSE)
