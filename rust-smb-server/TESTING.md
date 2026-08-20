# Rust SMB 3.1.1 服务器测试操作合集

## 环境信息

| 项目 | 值 |
|---|---|
| 服务器(Windows) | `10.27.139.50`,端口 `2445` |
| 客户端(Linux VM) | `10.27.139.16` |
| 共享 | `public`(匿名只读)、`home`(alice 读写) |
| 凭据 | 用户 `alice` / 密码 `password`(可用环境变量 `SMB_USER`/`SMB_PASS` 覆盖) |
| 共享根目录 | `D:\开源项目\OrbitCloud\rust-smb-server\share` |

---

## 一、Windows 侧操作

### 启动服务器

```powershell
cd D:\开源项目\OrbitCloud\rust-smb-server
.\target\release\rust-smb-server.exe
```

后台运行(带日志):

```powershell
Start-Process -FilePath "target\release\rust-smb-server.exe" `
  -WorkingDirectory "D:\开源项目\OrbitCloud\rust-smb-server" `
  -RedirectStandardOutput "server.log" -RedirectStandardError "server.err.log" `
  -WindowStyle Hidden
```

### 停止服务器

```powershell
Get-Process -Name "rust-smb-server" | Stop-Process -Force
```

### 重新编译

```powershell
rustup run stable cargo build --release
```

### 覆盖默认配置

```powershell
$env:SMB_LISTEN = "0.0.0.0:2445"   # 监听地址
$env:SMB_ROOT   = "D:\share"       # 共享根目录
$env:SMB_USER   = "alice"          # 用户名
$env:SMB_PASS   = "password"       # 密码
$env:RUST_LOG   = "info,smb_server=debug"  # 日志级别
```

### 防火墙(仅首次,需管理员)

```powershell
netsh advfirewall firewall add rule name="RustSMB2445" dir=in action=allow protocol=TCP localport=2445 profile=any
```

### 查看运行日志

```powershell
Get-Content server.log -Tail 50
```

---

## 二、Linux 侧验证(经 vmssh 或直接在 VM 上)

### 0. 前置:安装 smbclient(如未安装)

```bash
sudo apt install -y smbclient
```

### 1. 列出共享(验证连通性 + 认证)

```bash
smbclient -L //10.27.139.50 -p 2445 -U alice%password
```

预期:列出 `public` 与 `home` 两个共享。

### 2. 匿名连接只读共享

```bash
smbclient //10.27.139.50/public -p 2445 -N
```

预期:`Anonymous login successful`。

### 3. 认证连接读写共享(强制 SMB 3.1.1)

```bash
smbclient //10.27.139.50/home -p 2445 -U alice%password -m SMB3
```

交互内测试:

```
ls                     # 列出目录
get hello.txt          # 下载文件
put /etc/hostname t.txt   # 上传文件
mkdir dir1             # 创建目录
cd dir1                # 进入目录
put /etc/hostname in_dir.txt
cd ..                  # 返回上级
rename t.txt t2.txt    # 重命名
del t2.txt             # 删除文件
rmdir dir1             # 删除目录(需先删空其中文件)
quit                   # 退出
```

### 4. 一条命令完成自动化验证

```bash
smbclient //10.27.139.50/home -p 2445 -U alice%password -m SMB3 \
  -c 'ls;put /etc/hostname up.txt;ls;get up.txt;mkdir testdir;cd testdir;put /etc/hostname inner.txt;cd ..;del up.txt;rmdir testdir;ls'
```

预期:put/get/ls/del 全部成功,无 NT_STATUS 错误。

### 5. 确认协商方言为 SMB 3.1.1

```bash
smbclient //10.27.139.50/home -p 2445 -U alice%password -m SMB3 -d 3 -c 'ls' 2>&1 | grep -i dialect
```

预期输出包含 `Dialect = SMB 3.1.1`。

---

## 三、挂载测试(可选)

```bash
# 安装 cifs-utils
sudo apt install -y cifs-utils

# 挂载
sudo mkdir -p /mnt/smb
sudo mount -t cifs //10.27.139.50/home /mnt/smb \
  -o port=2445,username=alice,password=password,vers=3.1.1

# 验证
ls /mnt/smb
echo "test" | sudo tee /mnt/smb/test.txt
cat /mnt/smb/hello.txt

# 卸载
sudo umount /mnt/smb
```

---

## 四、通过 vmssh 执行(Windows 侧一键验证)

```powershell
$vmssh = "D:\开源项目\vmssh\vmssh.exe"

# 1. 列共享
& $vmssh -host 10.27.139.16 -user bury -pass 123 run `
  "smbclient -L //10.27.139.50 -p 2445 -U alice%password 2>&1"

# 2. 完整读写验证
& $vmssh -host 10.27.139.16 -user bury -pass 123 run `
  "smbclient //10.27.139.50/home -p 2445 -U alice%password -m SMB3 -c 'ls;put /etc/hostname up.txt;get up.txt;del up.txt;ls' 2>&1"

# 3. 确认方言
& $vmssh -host 10.27.139.16 -user bury -pass 123 run `
  "smbclient //10.27.139.50/home -p 2445 -U alice%password -m SMB3 -d 3 -c 'ls' 2>&1 | grep -i dialect"
```

---

## 五、故障排查速查

| 现象 | 可能原因 | 处理 |
|---|---|---|
| 服务器启动报 `AddrInUse`/端口占用 | 旧进程未退出 | `Get-Process rust-smb-server \| Stop-Process -Force`,或查 `netstat -ano \| Select-String ":2445"` 找到 PID 杀掉 |
| 服务器启动报 `Error: Os { code: 3 }` | share 目录未创建 | 已修复(main.rs 启动时自动 `create_dir_all`) |
| 客户端 `NT_STATUS_ACCESS_DENIED` 列目录 | 目录句柄未加 `FILE_FLAG_BACKUP_SEMANTICS` | 已修复(winfs.rs) |
| `SMB1 disabled` 提示 | 正常,客户端禁用 SMB1 而已 | 忽略 |
| 防火墙拦截 | 未放行 2445 | 用管理员执行 netsh 规则命令 |
| smbclient 连接超时 | VM 与 Windows 网络不通 | `ping 10.27.139.50` 检查 |

---

## 六、已知限制

- 仅支持 NTLM 认证(无 Kerberos/AD)
- `public` 共享为只读,`home` 共享为用户读写
- SMB 加密/压缩未启用(库未实现)
- 服务器为库实现,协议 3.1.1 核心功能(协商、认证、签名、读写、目录)已覆盖
