# OrbitCloud Docker Compose 部署(基于已编译二进制)

> 利用已编译的二进制(后端 / SMB 网关 / RustFS / 前端)以容器方式部署,无需源码构建。

## 架构

```
浏览器 ──► web(Nginx:18080) ──/api──► orbitcloud(:8080)
资源管理器 ──► smb-gateway(:1445→445) ──► orbitcloud(172.28.0.10:9101,私有协议)
                                        └──► rustfs(:9000,S3 对象存储)
                                              └── SQLite(命名卷持久化)
```

| 服务 | 容器端口 | 宿主默认映射 | 说明 |
|---|---|---|---|
| web | 80 | 18080 | Nginx 前端 + /api 反代(避开宿主 80=Gitea) |
| orbitcloud | 8080 / 9101 | 8080 | 后端 API + 私有 Socket(9101 仅容器内) |
| smb-gateway | 445 | 1445 | SMB 入口(避开宿主 445=系统 SMB) |
| rustfs | 9000 | 9000 | S3 对象存储(容器内网络名 rustfs) |

## 快速开始

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

## 配置与密钥

- 密钥由 `prep.ps1` 生成并写入 `.env`(JWT_SECRET / SHARED_KEY / S3 凭据),随后模板替换进两侧配置:
  - `orbitcloud/OrbitCloud.yaml`(由 `.tpl` 生成)→ 挂载到后端容器
  - `smb-gateway/config.yaml`(由 `.tpl` 生成)→ 挂载到网关容器
- 两侧 `shared_key` 一致,由 prep 保证;改密钥后重新运行 prep + `docker compose restart`
- 端口映射可在 `.env` 修改:PORT_WEB / PORT_SMB / PORT_API / PORT_S3

## 数据持久化(命名卷)

| 卷 | 挂载点 | 内容 |
|---|---|---|
| rustfs-data | /opt/rustfs/data | S3 对象数据 |
| orbitcloud-data | /opt/orbitcloud/data | SQLite 元数据库 |
| orbitcloud-logs | /opt/orbitcloud/logs | 后端日志 |
| smb-gateway-logs | /opt/orbitcloud/smb-gateway/logs | 网关日志 |

## 运维

```powershell
docker compose ps                # 状态
docker compose logs -f orbitcloud# 日志
docker compose restart           # 重启
docker compose down              # 停止(数据保留在卷中)
docker compose down -v           # 停止并清空数据
```

## 已知约束

- SMB 网关 Rust 侧 `gateway.addr` 只支持 IP:端口,故后端使用静态 IP `172.28.0.10`(compose 自定义网络 oc-net)
- 后端当前主线读取 `./OrbitCloud.yaml`(而非 config.yaml)
- 宿主 445 被系统 SMB 占用时,SMB 端口映射为 1445;Windows 资源管理器只能访问 445 端口,
  如需资源管理器直连,请将 PORT_SMB 改为 445 并释放宿主端口(或使用 Linux cifs 挂载 1445)
