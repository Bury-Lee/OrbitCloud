# SMB 网关 Linux 部署指南

> 本指南覆盖 SMB 网关(Go 服务端 + Rust 网关)在 **Linux** 上的构建、配置、部署与验证。
> 配套文档:`docs/smb-gateway/` 系列(设计总览、帧协议、连接生命周期、桶实例注册表等)。
>
> **实现状态(重要)**:帧协议、连接/握手、请求-响应转发链路已真实现并通过单测;
> 业务层(认证查询、文件操作落库/落对象)仍为占位,接入 DB/对象存储后按本指南即可上线。
> 部署形态按最终形态编写,标注 ⚠️ 的步骤在业务层落地前表现为"服务可启动、业务返回 NotImpl"。

## 1. 部署架构与端口

```text
┌────────────────────────────────────────────────────────────────┐
│ Linux 服务器 A(一体机部署,或拆分为多台)                            │
│                                                                │
│  OrbitCloud 主服务(Go)                                        │
│   ├─ HTTP 服务        :8080   (浏览器/API 客户端)               │
│   ├─ SMB 网关组件     :9001   (私有 Socket,仅本机 Rust 网关访问) │
│   └─ 对象存储(local 目录或远端 S3/RustFS)                        │
│                                                                │
│  Rust SMB 网关(smb-gateway 二进制)                             │
│   ├─ SMB 监听         :2445   (SMB 2.02~3.1.1,面向局域网客户端) │
│   └─ 出站连接         →127.0.0.1:9001(私有帧协议,共享密钥鉴权)  │
│                                                                │
│  Windows Explorer / Linux cifs 客户端 → \\服务器IP\桶名          │
└────────────────────────────────────────────────────────────────┘
```

| 端口 | 服务 | 监听范围 | 防火墙建议 |
| --- | --- | --- | --- |
| 8080 | OrbitCloud HTTP | 0.0.0.0 | 按需开放(或反代) |
| 9001 | SMB 网关私有 Socket | **127.0.0.1 仅本机** | 禁止对外 |
| 2445 | SMB 协议 | 0.0.0.0(内网) | 内网开放(或改用 445) |

> 端口调整:HTTP 在 `config.yaml` 的 `server` 分节;SMB 在 Rust 侧 `smb.listen`;私有 Socket 在 OrbitCloud `smb_gateway.listen_addr` 与 Rust 侧 `gateway.addr`(两侧必须一致)。

## 2. 前置条件

| 组件 | 版本 | 用途 |
| --- | --- | --- |
| Linux 系统 | x86_64 / arm64 | 部署平台(建议 Debian/Ubuntu 系) |
| Go 工具链 | ≥ 1.25 | 构建 OrbitCloud(含网关组件) |
| Rust 工具链 | ≥ 1.95 | 构建 smb-gateway(cargo) |
| Node.js | ≥ 18(可选) | 构建前端(可选) |
| 对象存储 | local 目录 或 S3 兼容服务 | 文件实体存储 |

Ubuntu 安装工具链示例:

```bash
# Go 1.25+(示例为 1.25.x,按需替换版本)
wget https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.25.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc && source ~/.bashrc

# Rust 1.95+(rustup 安装)
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
source "$HOME/.cargo/env"
```

## 3. 构建

### 3.1 OrbitCloud 主服务(含 SMB 网关组件)

```bash
cd /opt/src/orbitcloud            # 源码目录(仓库根)

# 后端(三平台可交叉编译;此处取 linux/amd64)
./build/build.sh --os linux --arch amd64
# 产物:build/dist/orbitcloud-linux-amd64(或直接 go build -o orbitcloud .)

# 前端(可选)
./build/build.sh --frontend       # 产物附 web/ 目录
```

### 3.2 Rust SMB 网关

```bash
cd /opt/src/orbitcloud/smb_server/rust
# Linux 原生构建(发布版:lto + strip 已配置)
cargo build --release
# 产物:target/release/smb-gateway
```

### 3.3 产物部署目录

```text
/opt/orbitcloud/
├── orbitcloud            # OrbitCloud 主服务二进制
├── config.yaml           # OrbitCloud 配置(含 smb_gateway 分节,见 §4)
├── web/                  # 前端静态资源(可选)
└── smb-gateway/          # Rust 网关
    ├── smb-gateway       # 二进制
    └── config.yaml       # 网关配置(见 §4)
```

## 4. 配置准备

### 4.1 生成共享密钥(两侧一致,≥16 字节)

```bash
openssl rand -hex 32        # 示例输出:4f6a...;记下并注入环境变量,禁止写入配置文件
```

### 4.2 OrbitCloud 配置(config.yaml 追加 `smb_gateway` 分节)

```yaml
server:
  host: 0.0.0.0
  port: 8080
storage:
  driver: local              # 未部署 S3 时用本地目录即可跑通
  endpoint: /var/lib/orbitcloud/data
# ↓ 新增:SMB 网关分节(与 Rust 侧 gateway 分节对应)
smb_gateway:
  listen_addr: "127.0.0.1:9001"                 # 私有 Socket,仅本机
  shared_key_env: "ORBITCLOUD_SMB_GATEWAY_KEY"  # 密钥环境变量名
  channel_buffer: 1024                          # 请求池队列深度(与 Rust 侧对齐)
  max_concurrent: 64                            # 请求池并发上限
```

启动时注入密钥:

```bash
export ORBITCLOUD_SMB_GATEWAY_KEY="4f6a...(上一步生成的密钥)"
```

### 4.3 Rust 网关配置(smb-gateway/config.yaml)

```yaml
smb:
  listen: "0.0.0.0:2445"        # SMB 监听;root 权限下可改 "0.0.0.0:445"
  netbios_name: "ORBITCLOUD"
gateway:
  addr: "127.0.0.1:9001"        # 必须与 OrbitCloud smb_gateway.listen_addr 一致
  shared_key_env: "ORBITCLOUD_SMB_GATEWAY_KEY"  # 同一密钥
  heartbeat_secs: 30
  sync_interval_secs: 60
  channel_buffer: 1024          # 发送管道缓冲(与 Go 侧队列深度对齐)
log:
  level: "info"
  output: "/var/log/orbitcloud/smb-gateway"     # 空 = 仅控制台
```

启动时注入同一密钥:

```bash
export ORBITCLOUD_SMB_GATEWAY_KEY="4f6a...(与主服务相同)"
```

## 5. 部署 OrbitCloud 主服务

### 5.1 初始化

```bash
sudo mkdir -p /var/lib/orbitcloud/data /var/log/orbitcloud
sudo useradd -r -s /usr/sbin/nologin orbitcloud || true
sudo chown -R orbitcloud:orbitcloud /opt/orbitcloud /var/lib/orbitcloud /var/log/orbitcloud

cd /opt/orbitcloud
# 首次:生成默认配置并创建超级管理员(HTTP 管理入口用)
sudo -u orbitcloud ./orbitcloud -initConfig
sudo -u orbitcloud ./orbitcloud --add-superadmin admin '你的强密码'
```

### 5.2 systemd 服务(HTTP + SMB 网关组件同进程)

`/etc/systemd/system/orbitcloud.service`:

```ini
[Unit]
Description=OrbitCloud (HTTP + SMB Gateway)
After=network-online.target

[Service]
User=orbitcloud
WorkingDirectory=/opt/orbitcloud
Environment=ORBITCLOUD_SMB_GATEWAY_KEY=4f6a...
ExecStart=/opt/orbitcloud/orbitcloud
Restart=on-failure
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now orbitcloud
sudo systemctl status orbitcloud          # 应显示 active;日志见 journalctl -u orbitcloud -f
```

> ⚠️ 当前阶段:SMB 网关组件的业务处理函数为占位,服务可启动、Socket 可连接,
> 但文件操作/认证请求会返回"NotImpl"哨兵。DB/对象存储接入后即生效。

## 6. 部署 Rust SMB 网关

### 6.1 二进制与配置就位

```bash
sudo mkdir -p /opt/orbitcloud/smb-gateway
sudo cp /opt/src/orbitcloud/smb_server/rust/target/release/smb-gateway /opt/orbitcloud/smb-gateway/
sudo cp /opt/src/orbitcloud/smb_server/rust/config.yaml /opt/orbitcloud/smb-gateway/
sudo chown -R orbitcloud:orbitcloud /opt/orbitcloud/smb-gateway
```

### 6.2 systemd 服务

`/etc/systemd/system/smb-gateway.service`:

```ini
[Unit]
Description=OrbitCloud SMB Gateway (Rust)
After=network-online.target orbitcloud.service
Wants=orbitcloud.service

[Service]
User=orbitcloud
WorkingDirectory=/opt/orbitcloud/smb-gateway
Environment=ORBITCLOUD_SMB_GATEWAY_KEY=4f6a...
ExecStart=/opt/orbitcloud/smb-gateway/smb-gateway
Restart=on-failure
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now smb-gateway
sudo systemctl status smb-gateway
```

> 若需要监听 445(标准 SMB 端口):`config.yaml` 改 `smb.listen: "0.0.0.0:445"`,
> 并给 systemd 服务加 `AmbientCapabilities=CAP_NET_BIND_SERVICE`(或按 2445 部署免特权)。

## 7. 防火墙与安全

```bash
# 仅开放必要的端口(示例:内网网段 192.168.1.0/24)
sudo ufw allow from 192.168.1.0/24 to any port 2445 proto tcp   # SMB
sudo ufw allow 8080/tcp                                          # HTTP(或交反代)
# 9001 禁止对外:仅 127.0.0.1 监听,无需放行
sudo ufw status
```

安全要点:

1. **9001 是私有通道**:必须只监听 127.0.0.1,共享密钥走环境变量注入,不落配置文件;
2. **密钥轮换**:更换 `ORBITCLOUD_SMB_GATEWAY_KEY` 后重启两侧服务即可(握手失败自动拒绝);
3. 生产建议对象存储用 S3 兼容服务(RustFS/MinIO),`storage.driver: s3`,数据不落本地盘;
4. NT hash(`users.nt_hash` 列)只经私有通道下发,日志不得打印密钥与哈希。

## 8. 客户端连接验证

### 8.1 Linux 客户端(cifs-utils)

```bash
sudo apt install -y cifs-utils
# 共享名 = 桶名(需先在 OrbitCloud 创建桶并建用户)
mkdir -p ~/mnt/bucketA
sudo mount -t cifs //服务器IP/桶A ~/mnt/bucketA \
  -o username=alice,password=密码,vers=3.1.1,uid=$(id -u),gid=$(id -g)
ls ~/mnt/bucketA        # 应看到桶内目录结构
# 卸载: sudo umount ~/mnt/bucketA
```

### 8.2 Windows 客户端

```text
Win+R → \\服务器IP\桶A → 输入 OrbitCloud 用户名/密码
```

> 共享可见性:用户只能看到自己被授权(可见性 ACL)的桶——其余桶名在 `net view` / 枚举中不出现。

## 9. 运维与故障排查

| 现象 | 排查 | 修复 |
| --- | --- | --- |
| smb-gateway 起不来"shared key not set" | 环境变量未注入 | systemd 的 Environment 行检查 |
| 握手失败/连接被拒 | 密钥两侧不一致;9001 被防火墙挡 | 核对密钥;`ss -ltnp \| grep 9001` |
| 客户端能连 SMB 但操作报错 | 业务层占位(当前阶段) | 见 §1 实现状态;接入 DB/存储后生效 |
| 客户端看不到任何共享 | 桶未创建/用户无 ACL | OrbitCloud 建桶 + 授权,等待推送(≤60s 对账) |
| 写文件后 stat 大小不对 | 写回缓存未 flush | 客户端主动 flush/close 触发整体上传 |
| 网络抖动后自动恢复? | 心跳 30s/空闲 90s | 断线自动重连 + 全量快照,无需人工 |

日志位置:

```bash
journalctl -u orbitcloud -f      # Go 主服务
journalctl -u smb-gateway -f     # Rust 网关
ls /var/log/orbitcloud/          # 落盘日志(按 config.yaml log.output)
```

## 10. 上线前检查清单(业务层落地后)

- [ ] `users.nt_hash` 列迁移完成,密码流程写入 NT hash
- [ ] 对象存储连通(Ping 通过),桶/文件操作返回真实数据
- [ ] 双向握手 + 变更推送验证(建桶 → 客户端立即可见)
- [ ] 写入-关闭-再读一致性验证(写回缓存整体上传)
- [ ] 防火墙:2445 开放、9001 仅本机
- [ ] systemd 开机自启 + 崩溃自动重启验证
