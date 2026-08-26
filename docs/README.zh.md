# OrbitCloud

> [English](../README.md) | 简体中文

OrbitCloud 是一个基于 **Go + Vue3** 的分布式网盘系统:元数据由 GORM 管理(SQLite 开发 / PostgreSQL 生产,支持读写分离),文件数据通过 **S3 协议** 写入对象存储(兼容 RustFS / MinIO 等),支持用户与权限、用户组、存储桶、目录树、文件管理、分享、批量操作与浏览器在线预览,并提供 **SMB 网关**(Windows 资源管理器直接挂载)。

## 功能特性

- **账号与权限**:JWT 双令牌 + 刷新令牌轮换;注册/用户管理;分级权限模型(0 超级管理员 / 1 管理员 / 2 特殊用户预留 / 3 普通用户,可扩展);用户组 + 条目级可见性 ACL
- **存储与文件**:桶管理;目录树模型(mkdir -p 自动建父链);上传/批量上传/流式下载/在线预览/重命名/移动/复制/删除;后台任务处理文件夹级联删除与复制(支持崩溃续跑);批量操作(≤100 条,逐条独立处理);桶容量配额;对象存储双驱动(`s3` / `local`)
- **分享**:限时 / 限次 / 提取码,支持文件与文件夹
- **SMB 访问**:Rust SMB 网关(445)+ 私有帧协议,共享名 = 桶名,Windows 资源管理器 / `net use` / Linux cifs 直接挂载
- **平台能力**:配置驱动(内置 embed 默认配置);GORM AutoMigrate 自动建表;读写分离(dbresolver);日志轮转 + 可选入库;定时清理任务;全局协程池;优雅停机;命令行指令(`-initConfig` / `--add-superadmin` / `--version` / `--help`)

## 技术栈

| 层 | 选型 |
|---|---|
| 语言 | Go 1.25+ / Rust 1.95+(SMB 网关) |
| HTTP | gin |
| JWT | golang-jwt/jwt/v5 |
| ORM | gorm + dbresolver |
| 数据库 | SQLite(开发)/ PostgreSQL(生产) |
| 对象存储 | minio-go/v7(S3)或本地目录驱动 |
| SMB | ixr-smb-server(Rust) |
| 前端 | Vue 3 + Vite + TS + Element Plus + Pinia |

## 快速开始(推荐:Docker Compose 二进制部署)

> 仓库已提供基于**已编译二进制**的容器化部署,无需源码构建,几分钟起全套(后端 + SMB 网关 + RustFS + 前端)。

```powershell
# 1. 准备:复制二进制到构建上下文 + 生成随机密钥配置(详见 docker/README.md)
powershell -ExecutionPolicy Bypass -File docker\prep.ps1

# 2. 构建并启动
docker compose -f docker\docker-compose.yml up -d --build

# 3. 创建超级管理员(仅首次)
docker compose -f docker\docker-compose.yml exec orbitcloud /usr/local/bin/orbitcloud --add-superadmin admin <密码≥8位>

# 4. 访问
#    网页   : http://127.0.0.1:18080
#    API   : http://127.0.0.1:8080/api/v1
#    SMB   : \\127.0.0.1\<桶名>(端口按 docker\.env 的 PORT_SMB,默认 1445)
```

详见 [docker/README.md](docker/README.md)(架构、密钥、数据卷、运维命令与已知约束)。

## 部署方式对比

| 方式 | 说明 | 适合 |
|---|---|---|
| **Docker Compose(二进制)** | 免编译,4 容器一键起 | 快速体验 / 私有化交付 |
| systemd 手动部署 | Linux 服务器传统部署(后端 + RustFS + SMB + Nginx) | 生产单机 |
| 部署运维工具 | 单窗口桌面工具:审计 → 预检 → 生成确定性部署脚本 | 运维人员 |

### systemd 部署(手动)

```bash
# 服务器准备 Go ≥1.25 工具链后(或直接用 dist/ 现成二进制):
sudo bash deploy/deploy.sh --superadmin admin <密码>
# 前端(需 Nginx + Node):deploy/deploy.sh --only-frontend
```

### 部署运维工具(deploy-tool)

`OrbitCloudDeployTool.exe` 是独立单窗口桌面工具(Go + WebView2,~12MB,免安装):

- **审计**(只读):环境 / 服务 / 端口 / 两侧配置一致性(shared_key、jwt.secret、日志级别、存储驱动),自动产出警告与建议(可勾选为待办)
- **预检**:磁盘 / 工具链 / 目录 / 健康,输出 PASS/WARN;**LSP 式实时预检**——修改表单即自动防抖检查
- **脚本生成**:参数化表单(下拉/填值/开关)→ 校验不恰当配置(error 阻断 / warn 提示)→ 生成**确定值** `.sh`(密钥自动生成并写入,可完整预览/复制/保存)
- **原生 SSH**:内置 SSH 通道(凭据兼容 sshbox.json),不依赖外部程序;部署脚本内嵌于二进制,工具完全自包含

详见 [deploy-tool/README.md](deploy-tool/README.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [部署指南](docs/部署指南.md) | 快速开始 / 生产部署(Linux systemd、Windows NSSM、Docker)/ 对象存储 / Nginx / 运维 |
| [部署与 Windows 挂载手册](docs/部署与Windows挂载手册.md) | 服务器一键部署 + Windows 资源管理器挂载 SMB 全流程 |
| [配置参考](docs/配置参考.md) | `OrbitCloud.yaml` 全字段说明、环境变量覆盖、生产最小改动清单 |
| [API 参考](docs/API参考.md) | 全部端点、请求/响应示例、错误码与鉴权要点 |
| [架构设计](docs/架构设计.md) | 分层、数据模型、权限模型、存储设计、关键流程 |
| [构建与发布](docs/构建与发布.md) | 交叉编译脚本用法、打包、版本管理 |
| [开发指南](docs/开发指南.md) | 环境准备、分层铁律、新增 API 流程、测试与提交规范 |

## 构建(一键三平台)

| 平台 | 脚本 | 产物 |
|---|---|---|
| Linux / macOS / WSL | `bash build/build.sh --all --frontend` | `build/dist/` |
| 任意(Git Bash) | 见 [构建与发布](docs/构建与发布.md) | — |

也可直接使用仓库根 `dist/` 下的**现成产物**(已编译):三平台后端、双平台 SMB 网关、前端 `web/`,含 `sha256sums.txt` 校验。

```bash
# 编译当前平台后端 + SMB 网关 + 前端(Go/Rust/Node 工具链)
./build/build.sh --all --frontend
```

## 命令行指令

| 指令 | 说明 |
|---|---|
| `orbitcloud` | 正常启动服务 |
| `orbitcloud -initConfig` | 用内置默认配置强制覆盖生成 `./OrbitCloud.yaml` 后退出 |
| `orbitcloud --add-superadmin <username> <password>` | 创建权限 0 超级管理员后退出(仅此通道可创建;密码 ≥8 位;不幂等) |
| `orbitcloud --version` | 打印版本号后退出 |
| `orbitcloud --help` / `-h` | 打印帮助后退出 |

## 目录结构

```
├── main.go                 # 入口:配置→初始化→HTTP 服务→后台任务→优雅停机
├── config/                 # 配置结构 + 内置默认配置(OrbitCloud.yaml, embed)
├── appinit/                # 初始化程序(配置/日志/DB/JWT/存储/协程池)
├── core/                   # 全局单例 + JWT 服务 + 对象存储抽象
├── common/                 # 哨兵错误/统一响应/分页/路径与名校验
├── model/                  # GORM 模型定义
├── server/                 # 业务层(包级函数)
├── api/                    # HTTP 层(路由/handler/中间件)
├── cron/                   # 定时任务
├── flag/                   # 命令行指令
├── log/                    # 日志封装(轮转、可选入库)
├── utils/                  # 桶名编码/采样 MD5/范围解析
├── smb_server/             # SMB 网关(go/ 内部组件 + rust/ 独立网关)
├── frontend/               # Vue3 前端工程
├── build/                  # 构建脚本(三平台交叉编译)
├── deploy/                 # 部署脚本(systemd)
├── docker/                 # Docker Compose 二进制部署(含 README)
├── deploy-tool/            # 部署运维工具(单窗口桌面,Go + WebView2)
├── dist/                   # 已编译产物(三平台后端/网关 + 前端 web/)
├── docs/                   # 文档
└── tem/                    # 临时研究与外部源码(如 sshbox)
```

## 分层约定

```
main(启动/优雅停机) → appinit(初始化) → core(全局单例)
api(HTTP 接入层:鉴权/校验/错误映射)
server(业务层:包级函数)
model(GORM 模型)  common(哨兵错误/分页/校验)  utils(桶名编码/采样 MD5)
```

- `core` 是最底层基础设施:只存放全局单例,不 import 上层业务包
- `server` 层为包级函数,函数内直接访问 `core` 单例
- `api` 层纯无状态:只做参数解析/鉴权/调用 server,把哨兵错误映射为 HTTP 状态码与统一响应体 `{code, data, message}`
- 对象键设计:文件实体在对象存储中的 key = 文件记录主键 ID,桶名 = `utils.BucketEncoder(桶ID)`(36 进制编码),不依赖文件名/路径

## API 概览

统一前缀 `/api/v1`;响应格式:成功 `{code:0, data:{...}, message:"ok"}`,错误 `{code:-1, message:"..."}`。

### 认证与用户

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/auth/login` | 公开 | 登录,返回 access/refresh token + user |
| POST | `/auth/refresh` | 公开 | 刷新令牌(轮换:旧令牌即刻失效) |
| POST | `/auth/register` | 管理员 | 注册用户(权限 0 不可经 API 创建) |
| POST | `/auth/logout` | 登录 | 吊销刷新令牌 |
| GET/PUT | `/users/me` | 登录 | 当前用户信息 / 改密码·名字·邮箱 |
| GET | `/users` | 管理员 | 用户分页列表 |
| PUT/DELETE | `/users/:id` | 管理员 | 改他人(权限/状态)/ 删除(软删) |

### 用户组

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/users/me/groups` | 登录 | 我的组 |
| POST/GET | `/groups`、`/groups/:id` | 管理员 | 组 CRUD |
| POST/DELETE | `/groups/:id/members`、`/groups/:id/members/:uid` | 管理员 | 添加 / 移除成员 |

### 桶

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST/GET | `/buckets` | 登录 | 创建 / 列表 |
| GET | `/buckets/:id` | 登录 | 桶详情(含配额/已用) |
| PUT/DELETE | `/buckets/:id` | 登录 | 修改 / 删除(后台任务级联清理) |

### 文件

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/buckets/:id/files` | 登录 | 上传(multipart 字段 `file`) |
| POST | `/buckets/:id/files/batch` | 登录 | 批量上传(字段 `files`) |
| GET | `/buckets/:id/files` | 登录 | 列表(分页) |
| GET | `/buckets/:id/files/:fid` / `/dirs/:fid` | 登录 | 文件 / 文件夹元数据 |
| GET | `/buckets/:id/files/:fid/download` | 登录 | 下载(流式) |
| GET | `/buckets/:id/files/:fid/preview` | 登录 | 在线预览 |
| POST | `/buckets/:id/files/:fid/copy` | 登录 | 复制文件 |
| POST | `/buckets/:id/files/:fid/move` | 登录 | 移动/重命名文件 |
| POST | `/buckets/:id/dirs/:fid/move` | 登录 | 移动/重命名文件夹 |
| DELETE | `/buckets/:id/files/:fid` | 登录 | 删除文件 |
| POST | `/buckets/:id/dirs` | 登录 | 新建文件夹(mkdir -p 建父链) |
| DELETE | `/buckets/:id/dirs/:fid` | 登录 | 删除文件夹(后台深度优先清理) |
| POST | `/buckets/:id/dirs/:fid/copy` | 登录 | 复制文件夹(落 CopyTask 后台处理) |
| POST | `/buckets/:id/items/batch-delete` / `batch-copy` / `batch-move` | 登录 | 批量操作(≤100 条) |
| GET | `/buckets/:id/items/batch-download` | 登录 | 批量下载(zip) |
| PUT | `/buckets/:id/files/:fid/visibility` / `/dirs/:fid/visibility` | 登录 | 设置条目可见组 |

### 分享

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/share/:token` | 公开 | 分享解析 / 下载(提取码走请求参数) |
| POST/GET | `/shares` | 登录 | 创建 / 列表 |
| PUT/DELETE | `/shares/:id` | 登录 | 修改 / 删除(仅创建者) |

## 权限模型

- `PermissionLevel` 数值越小权限越高;已定义 0 = 超级管理员、1 = 管理员、2 = 特殊用户(预留)、3 = 普通用户,可在 0~9 区间按需扩展更多档位
- 管理员判定统一为 `PermissionLevel.IsAdmin()`(即 0 / 1)
- 权限 0 只能经命令行 `--add-superadmin` 创建,HTTP API 一律归一为普通用户
- 管理员改他人权限/状态/删除时,操作者权限须严格高于目标
- 条目级可叠加用户组 ACL(`VisibleToGroups` JSON 组 ID 列表,空 = 不限制;非空 = 仅管理员/组内成员可见,上传者不自动放行)
- 桶级:访问等级与管理等级独立(管理等级缺省跟随访问等级,管理要求不得松于访问);配置 `bucket.admin_create_only` 可限制仅管理员建桶

## 后台任务与健壮性

- 删除/复制任务:目标先置不可用 → 落任务表 → 后台深度优先处理,"边删边查",支持进程崩溃后启动续跑
- 任务级乐观锁防重复处理;定时任务每小时清理过期日志/令牌/分享
- 上传补偿:对象写失败删记录、元数据更新失败清对象,不留空洞/孤儿
- 500 错误对外统一 "internal error";配置缺失/非法、存储不可用均启动即终止

## 许可

[Apache License 2.0](LICENSE)
