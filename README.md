# OrbitCloud

OrbitCloud 是一个基于 **Go + Vue3** 的分布式网盘系统:元数据由 GORM 管理(SQLite 开发 / PostgreSQL 生产,支持读写分离),文件数据通过 **S3 协议** 写入对象存储(兼容 RustFS / MinIO 等),支持用户与权限、用户组、存储桶、目录树、文件管理、分享、批量操作与浏览器在线预览。

## 功能特性

- **账号与权限**:JWT 双令牌 + 刷新令牌轮换;注册/用户管理;分级权限模型(0 超级管理员 / 1 管理员 / 2 特殊用户预留 / 3 普通用户,可扩展);用户组 + 条目级可见性 ACL
- **存储与文件**:桶管理;目录树模型(mkdir -p 自动建父链);上传/批量上传/流式下载/在线预览/重命名/移动/复制/删除;后台任务处理文件夹级联删除与复制(支持崩溃续跑);批量操作(≤100 条,逐条独立处理);桶容量配额;对象存储双驱动(`s3` / `local`)
- **分享**:限时 / 限次 / 提取码,支持文件与文件夹
- **平台能力**:配置驱动(内置 embed 默认配置);GORM AutoMigrate 自动建表;读写分离(dbresolver);日志轮转 + 可选入库;定时清理任务;全局协程池;优雅停机;命令行指令(`-initConfig` / `--add-superadmin` / `--version` / `--help`)

## 技术栈

| 层 | 选型 |
|---|---|
| 语言 | Go 1.25+ |
| HTTP | gin |
| JWT | golang-jwt/jwt/v5 |
| ORM | gorm + dbresolver |
| 数据库 | SQLite(开发)/ PostgreSQL(生产) |
| 对象存储 | minio-go/v7(S3)或本地目录驱动 |
| 前端 | Vue 3 + Vite + TS + Element Plus + Pinia |

## 文档

| 文档 | 内容 |
|---|---|
| [部署指南](docs/部署指南.md) | 快速开始 / 生产部署(Linux systemd、Windows NSSM、Docker)/ 对象存储 / Nginx / 运维 |
| [配置参考](docs/配置参考.md) | `config.yaml` 全字段说明、环境变量覆盖、生产最小改动清单 |
| [API 参考](docs/API参考.md) | 全部端点、请求/响应示例、错误码与鉴权要点 |
| [架构设计](docs/架构设计.md) | 分层、数据模型、权限模型、存储设计、关键流程 |
| [构建与发布](docs/构建与发布.md) | 三系统交叉编译脚本用法、打包、版本管理 |
| [开发指南](docs/开发指南.md) | 环境准备、分层铁律、新增 API 流程、测试与提交规范 |

## 构建(一键三系统)

Windows 下执行(详见 [构建与发布](docs/构建与发布.md)),交叉编译 **Windows / Linux / macOS** 三平台二进制,产物在 `build/dist/`:

```bat
rem 默认:三系统 × amd64/arm64 后端
build\build.bat
rem 同步构建 Vue 前端(附带至各包 web/)
build\build.bat -Frontend
rem 完整发布:构建前端并打 zip/tar.gz 包
build\build.bat -Frontend -Package
```

Linux / macOS 对等脚本:`./build/build.sh --frontend --package`。

## 快速开始(本地开发)

环境要求:Go ≥ 1.25(零 CGO 可构建)、Node.js ≥ 18(前端,可选)。

```bash
go build -o orbitcloud .

./orbitcloud -initConfig                    # 首次:生成 ./config.yaml(内置默认配置)
./orbitcloud --add-superadmin admin admin123456  # 创建超级管理员(仅一次)
./orbitcloud                                # 默认监听 0.0.0.0:8080
```

未启动对象存储时,修改 `config.yaml` 使用本地驱动即可跑通全流程:

```yaml
storage:
  driver: local        # 对象直接写本地目录
```

启动前端(可选):

```bash
cd frontend
npm install
npm run dev             # http://localhost:5173,Vite 代理 /api → 127.0.0.1:8080
```

## 配置

配置固定从工作目录 `./config.yaml` 读取;缺失时自动用内置默认配置生成,`-initConfig` 可强制覆盖。**不做缺省值兜底**——配置缺失/非法即启动报错。敏感项建议环境变量注入:`ORBITCLOUD_JWT_SECRET` / `ORBITCLOUD_DB_DSN` / `ORBITCLOUD_S3_ACCESS_KEY` / `ORBITCLOUD_S3_SECRET_KEY`。

## 命令行指令

| 指令 | 说明 |
|---|---|
| `orbitcloud` | 正常启动服务 |
| `orbitcloud -initConfig` | 用内置默认配置强制覆盖生成 `./config.yaml` 后退出 |
| `orbitcloud --add-superadmin <username> <password>` | 创建权限 0 超级管理员后退出(仅此通道可创建;密码 ≥8 位;不幂等) |
| `orbitcloud --version` | 打印版本号后退出 |
| `orbitcloud --help` / `-h` | 打印帮助后退出 |

## 目录结构

```
├── main.go                 # 入口:配置→初始化→HTTP 服务→后台任务→优雅停机
├── config/                 # 配置结构 + 内置默认配置(default.yaml, embed)
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
├── frontend/               # Vue3 前端工程
├── build/                  # 构建脚本(Windows 一键三系统交叉编译,产物在 build/dist/)
├── docs/                   # 文档(部署/配置/API/架构/构建/开发)
└── vendor/                 # Go 依赖(提交)
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
