# OrbitCloud Docker 部署

> 仓库提供 **两种** Docker 部署方式,请按需选用:
>
> | | 方式 A:单镜像(推荐) | 方式 B:多容器 Compose |
> |---|---|---|
> | compose 文件 | **`docker-compose.prod.yml`** | `docker-compose.yml` |
> | 镜像来源 | `orbitcloud:latest`(Dockerfile 构建的**单镜像**,含 后端+SMB网关+前端) | 仓库根 `Dockerfile` 之外的多容器(web + orbitcloud + smb-gateway + rustfs + postgres) |
> | 镜像获取 | `docker load -i orbitcloud.tar`(发行产物) | 需已编译二进制 + `prep.ps1` 准备 |
> | 配置方式 | **环境变量**(postgres 风格,60+ 参数) | 配置文件挂载 |
> | 适用场景 | 开箱即用、快速部署、对外分发 | 旧版多服务拆分(带独立 rustfs 对象存储/PostgreSQL) |

---

## 方式 A:单镜像(推荐,postgres 风格)

### 1. 获取镜像

```bash
# ① 从发行产物加载(Windows 侧 dist/orbitcloud.tar)
docker load -i orbitcloud.tar

# ② 或由源码构建
docker build -t orbitcloud:latest .
```

### 2. 配置(环境变量,全部可选,未设置用内置默认值)

参考 `docker-compose.prod.yml` 顶部注释的完整变量清单,生产至少改:

```yaml
environment:
  ORBITCLOUD_JWT_SECRET: "32字节以上随机串"     # ★必改
  ORBITCLOUD_SMB_SHARED_KEY: "32字节以上随机串"  # ★必改(与网关一致)
  ORBITCLOUD_STORAGE_DRIVER: local              # local | s3
  # 其余按需:ORBITCLOUD_LOG_LEVEL / ORBITCLOUD_DB_DSN / ...
```

### 3. 启动

```bash
docker compose -f docker-compose.prod.yml up -d

# 创建超级管理员(仅首次)
docker exec orbitcloud /usr/local/bin/orbitcloud --add-superadmin admin <密码≥8位>

# 访问
#   网页: http://127.0.0.1:80
#   API : http://127.0.0.1:8080/api/v1
#   SMB : \\127.0.0.1(445)
```

### 4. 运维

```bash
docker compose -f docker-compose.prod.yml ps          # 状态
docker compose -f docker-compose.prod.yml logs -f orbitcloud
docker compose -f docker-compose.prod.yml restart
docker compose -f docker-compose.prod.yml down -v     # 停止并清空数据
```

### 5. 数据

- 单卷 `orbitcloud-data:/data`:SQLite 元数据库 + local 对象存储 + 日志,删卷即重置

### 6. 高度定制(进阶)

单镜像内配置模板由环境变量渲染,覆盖全部 60+ 参数;
多节点/读写分离等超定制场景可挂载自定义 yaml 覆盖模板:

```yaml
volumes:
  - ./OrbitCloud.yaml:/opt/orbitcloud/OrbitCloud.yaml
  - ./SMB.yaml:/opt/orbitcloud/gateway/SMB.yaml
```

完整注释版参考 `templates/OrbitCloud.yaml.example` 与 `templates/SMB.yaml.example`。

---

## 方式 B:多容器 Compose(旧版,基于已编译二进制)

> 若你维护旧版多服务拓扑(独立 rustfs 对象存储 + PostgreSQL + 独立前端容器),
> 使用本方式。

### 快速开始

```powershell
# 1. 准备:复制二进制(dist 三件套 + other_software/rustfs)+ 生成 .env 与配置(随机密钥)
powershell -ExecutionPolicy Bypass -File prep.ps1

# 2. 构建并启动
docker compose up -d --build

# 3. 创建超级管理员(仅首次)
docker compose exec orbitcloud /usr/local/bin/orbitcloud --add-superadmin admin <密码≥8位>

# 4. 访问
#    网页: http://127.0.0.1:18080
#    API : http://127.0.0.1:8080/api/v1
```

### 架构

```
浏览器 ──► web(Nginx:18080) ──/api──► orbitcloud(:8080)
资源管理器 ──► smb-gateway(:1445→445) ──► orbitcloud(172.28.0.10:9101,私有协议)
                                        └──► rustfs(:9000,S3 对象存储)
                                              └── SQLite(命名卷持久化)
```

### 配置与密钥

- 密钥由 `prep.ps1` 生成并写入 `.env`(JWT_SECRET / SHARED_KEY / S3 凭据),随后模板替换进两侧配置:
  - `orbitcloud/OrbitCloud.yaml`(由 `.tpl` 生成)→ 挂载到后端容器
  - `smb-gateway/config.yaml`(由 `.tpl` 生成)→ 挂载到网关容器
- 两侧 `shared_key` 一致,由 prep 保证;改密钥后重新运行 prep + `docker compose restart`
- 端口映射可在 `.env` 修改:PORT_WEB / PORT_SMB / PORT_API / PORT_S3

### 数据持久化(命名卷)

| 卷 | 挂载点 | 内容 |
|---|---|---|
| rustfs-data | /opt/rustfs/data | S3 对象数据 |
| orbitcloud-data | /opt/orbitcloud/data | SQLite 元数据库 |
| orbitcloud-logs | /opt/orbitcloud/logs | 后端日志 |
| smb-gateway-logs | /opt/orbitcloud/smb-gateway/logs | 网关日志 |

### 已知约束

- SMB 网关 Rust 侧 `gateway.addr` 只支持 IP:端口,故后端使用静态 IP `172.28.0.10`(compose 自定义网络 oc-net)
- 后端当前主线读取 `./OrbitCloud.yaml`(而非 config.yaml)
- 宿主 445 被系统 SMB 占用时,SMB 端口映射为 1445;Windows 资源管理器只能访问 445 端口,
  如需资源管理器直连,请将 PORT_SMB 改为 445 并释放宿主端口(或使用 Linux cifs 挂载 1445)

---

## 如何选择

- **新部署 / 对外分发** → 方式 A:`docker-compose.prod.yml`(单镜像,环境变量配置,一条命令启动)
- **需要独立 rustfs / PostgreSQL 的多服务拓扑** → 方式 B:`docker-compose.yml`
- 不确定选哪个 → 选方式 A。
