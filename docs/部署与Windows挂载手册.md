# OrbitCloud 部署与 Windows 挂载手册

> 适用场景:Ubuntu 24.04 服务器(systemd)+ Windows 客户端。本文以测试环境
> `<服务器IP>`(用户 `<用户名>`,sudo 免密)为例。全流程约 10 分钟,分三步:
> **① Windows 侧推送源码 → ② 服务器一键部署脚本 → ③ Windows 挂载 SMB**。

---

## 0. 架构一览

```
Windows 客户端
   ├── 浏览器 → http://<服务器>/          Nginx(80) → 前端 web/ + /api 反代 :8080
   └── 资源管理器 \\<服务器>\<桶名>  ──►  smb-gateway(445) ── 私有帧协议 ──► orbitcloud(8080)
                                                                   ├── RustFS(9000, S3 对象存储)
                                                                   └── SQLite / PostgreSQL(元数据)
```

| 组件 | 端口 | 说明 |
|---|---|---|
| orbitcloud(Go) | 8080 | HTTP API;9101 为 Go↔Rust 私有 Socket |
| smb-gateway(Rust) | 445 | SMB 协议入口,共享名 = 桶名 |
| RustFS | 9000 | S3 协议对象存储(凭据 `rustfsadmin`/`rustfsadmin`) |
| Nginx | 80 | 前端静态资源 + `/api` 反代 |

---

## 1. Windows 侧准备

| 物料 | 位置 | 说明 |
|---|---|---|
| 项目源码 | 如 `D:\开源项目\OribitCloud-SMB` | 本手册以该目录为例 |
| `other_software/` | 仓库内 | Go / Rust 工具链 tar 包、rustfs 二进制(部署到服务器用) |
| `tool/sshbox.exe` | 仓库内 | SSH 工具(推送源码 / 远程执行),配置见 `tool/sshbox.json` |

sshbox 常用命令:

```
sshbox run "命令"              远程执行命令
sshbox push <本地> <远端目录>   递归推送文件/目录
sshbox get / set               查看/修改连接配置(host/user/pass/port)
```

---

## 2. 推送源码到服务器(Windows 侧)

> 建议同步源码时排除体积大且无用的目录,只推源文件(约 1.6 MB):

`.git`、`target`、`node_modules`、`frontend/dist`、`other_software`、`tool`、`logs`、`log`、`data`、`build/dist`

```
# 在项目根目录执行(以推送整个仓库到服务器桌面为例)
tool\sshbox.exe push <本地仓库路径>\ <远端目录>
```

> ⚠️ 注意两点:
> - 远端目录用绝对路径(如 `<仓库所在目录>/`),不要写 `~/...`(sshbox 会按字面量拼接);
> - **`log/` 是 Go 源码包,不能排除**(与日志目录 `logs/` 不同),漏推会导致
>   `package orbitcloud/log is not in std` 构建失败。

---

## 3. 服务器一键部署(Linux 侧)

把 `build/deploy.sh` 放到服务器源码根(或任意位置),赋予执行权限后运行:

```bash
chmod +x build/deploy.sh

./deploy/deploy.sh                          # 默认源码目录 ~/桌面/OrbitCloud
./deploy/deploy.sh /opt/src/OrbitCloud      # 指定源码目录
./deploy/deploy.sh --check                  # 仅环境检查
```

### 脚本自动完成(幂等,重复执行安全)

| 步骤 | 行为 |
|---|---|
| 工具链 | 系统无 Go(≥1.25)/ Rust(≥1.95)时,自动解压 `other_software/` 内的 tar 包安装 |
| 网络镜像 | `proxy.golang.org` / `crates.io` 不可达时自动切换 `goproxy.cn` / `rsproxy.cn` |
| RustFS | 缺失时把 `other_software/rustfs` 装到 `~/rustfs`,建 `/opt/rustfs/data` + systemd 单元 |
| 构建 | Go `orbitcloud`、Rust `smb-gateway`(release),服务器有 Node 时顺带构建前端 |
| 部署 | 备份旧二进制 → 安装到 `/opt/orbitcloud/{bin,smb-gateway,web}` |
| 配置 | 无 `config.yaml` 时 `-initConfig` 自动生成 |
| systemd | 无单元文件时创建 `orbitcloud.service` / `smb-gateway.service`,**自动生成随机密钥**(JWT + SMB 共享密钥,两侧一致) |
| 验证 | 打印服务状态、端口监听、Web/SMB 访问方式 |

### 首次部署后必做

```bash
# 1. 按需修改 /opt/orbitcloud/config/config.yaml(生产必改 jwt.secret / storage 凭据)
# 2. 创建超级管理员(密码 ≥ 8 位)
sudo -u <用户名> /opt/orbitcloud/bin/orbitcloud --add-superadmin admin '<强密码>'
# 3. 重启使配置生效
sudo systemctl restart orbitcloud smb-gateway
```

---

## 4. 部署验证

```bash
systemctl is-active orbitcloud smb-gateway rustfs nginx   # 应全为 active
ss -tlnp | grep -E ':(80|445|8080|9000|9101)'
curl -s -X POST http://127.0.0.1:8080/api/v1/auth/login \
     -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"错误密码"}'     # 应返回 {"code":-1,...}
tail -f /opt/orbitcloud/smb-gateway/logs/* 2>/dev/null   # Rust 网关日志
sudo journalctl -u orbitcloud -f                          # Go 主服务日志
```

---

## 5. Windows 挂载 SMB

**前置条件**:已创建桶(如 `test-bucket`)且当前用户可见;服务器 445 端口可达
(同一局域网,关闭防火墙或放行)。

### 方式 A:资源管理器(推荐)

1. 打开文件资源管理器,地址栏输入 `\\<服务器IP>\test-bucket` 回车;
2. 弹出凭据窗口,输入 OrbitCloud 账号密码(如 `admin` / 密码);
3. 挂载成功后可像本地磁盘一样浏览、读写文件。

> 共享名 = 桶名。想看到哪个桶就访问 `\\服务器IP\<桶名>`。

### 方式 B:命令行 `net use`(持久化映射盘符)

```bat
:: 挂载(持久化:重启后自动重连;示例账号 admin,替换为实际密码)
net use Z: \\<服务器IP>\test-bucket /user:admin <密码> /persistent:yes

:: 验证挂载(应列出桶内目录/文件)
dir Z:\

:: 断开映射
net use Z: /delete

:: 查看所有已映射连接
net use
```

常用辅助命令:

```bat
:: 清除记住的错误凭据后重新连接
cmdkey /delete:<服务器IP>
net use Z: /delete
net use Z: \\<服务器IP>\test-bucket /user:admin <密码> /persistent:yes

:: 连接前先确认 445 端口可达(测试服务器 SMB 服务)
Test-NetConnection <服务器IP> -Port 445

:: 每次服务端(smb-gateway/orbitcloud)重启后连接被踢时,重挂一次即可
net use Z: /delete /y
net use Z: \\<服务器IP>\test-bucket /user:admin <密码> /persistent:yes
```

### 方式 C:Linux cifs 挂载(可选)

```bash
sudo mount -t cifs //<服务器IP>/test-bucket /mnt/disk \
     -o username=admin,password=<密码>,vers=3.1.1
```

---

## 6. 常见问题排查

| 现象 | 排查 |
|---|---|
| smb-gateway 反复 "challenge digest mismatch" | Go 与 Rust 两侧 `ORBITCLOUD_SMB_GATEWAY_KEY` 不一致,核对 systemd 单元 |
| 资源管理器挂载失败 | 桶不存在或当前用户不可见;先建桶再挂载;确认 445 可达(`Test-NetConnection <服务器IP> -Port 445`) |
| 网页打开 502 | nginx `/api` 反代指向 127.0.0.1:8080 是否正确;`client_max_body_size` 是否够大 |
| 构建报 `package orbitcloud/log is not in std` | 源码同步时漏推了 `log/` 源码包 |
| 构建下载超时 | 网络受限,`GOPROXY=https://goproxy.cn,direct`;cargo 用 rsproxy.cn 镜像(脚本已自动处理) |
| 上传成功但列表看不到 | 桶 ACL / 可见组设置;检查 `visibility` 接口 |
| 服务起不来、日志有 ERROR | `sudo journalctl -u orbitcloud -f` 看具体报错;config.yaml 缺失时先 `-initConfig` |

---

## 附录 A:手动部署(不用一键脚本时)

```bash
# 1. 工具链(用 other_software 内的 tar 包)
mkdir -p ~/go-toolchain && tar -C ~/go-toolchain -xzf go1.26.5.linux-amd64.tar.gz
mkdir -p ~/rust-toolchain && tar -C ~/rust-toolchain -xJf rust-1.97.1-x86_64-unknown-linux-gnu.tar.xz
cd ~/rust-toolchain/rust-*/ && ./install.sh --prefix=$HOME/rust-toolchain/installed

# 2. 构建
export PATH=~/go-toolchain/go/bin:~/rust-toolchain/installed/bin:$PATH
export GOPROXY=https://goproxy.cn,direct CARGO_HOME=~/.cargo-home
cd ~/桌面/OrbitCloud && go build -trimpath -o orbitcloud .
cd ~/桌面/OrbitCloud/smb_server/rust && cargo build --release

# 3. 部署
sudo mkdir -p /opt/orbitcloud/{bin,config,logs,smb-gateway}
sudo cp ~/桌面/OrbitCloud/orbitcloud /opt/orbitcloud/bin/
sudo cp ~/桌面/OrbitCloud/smb_server/rust/target/release/smb-gateway /opt/orbitcloud/smb-gateway/
cd /opt/orbitcloud/config && sudo -u <用户名> /opt/orbitcloud/bin/orbitcloud -initConfig

# 4. 密钥(两侧必须一致,≥16 字节)
openssl rand -hex 32    # 填入 systemd Environment=ORBITCLOUD_SMB_GATEWAY_KEY

# 5. systemd 单元:见 build/deploy.sh 内模板,或直接跑脚本
```

## 附录 B:更新部署(已有环境)

```bash
./deploy/deploy.sh            # 一键:重新构建 + 备份 + 热更新 + 重启
```

前端更新:本地 `npm run build` 后推送 `frontend/dist` 到服务器替换
`/opt/orbitcloud/web`(或服务器装 Node 后由脚本自动构建)。
