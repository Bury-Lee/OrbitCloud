# OrbitCloud Docker 镜像打包教程

> 本文档记录在远程 Linux 机器上用 `sshbox` 完成 Docker 镜像打包的完整流程,
> 供下一次打包直接照做。适用场景:Windows 开发机 → sshbox → Linux 打包机 → 导出镜像回 Windows。

## 0. 流程总览

```
① 同步代码(sshbox push)  →  ② 检查更新(对比 git + md5)  →  ③ 写/更新 Dockerfile
→ ④ 准备 Docker(安装 + 镜像加速)  →  ⑤ docker build(后台执行)  →  ⑥ 验证(健康检查)
→ ⑦ docker save 导出  →  ⑧ sshbox pull 拉回 Windows  →  ⑨ 分发
```

## 1. 前置条件

| 项 | 说明 |
|---|---|
| 远程 Linux 机器 | 本文档示例 `192.168.10.107`,用户 `bury` |
| `tool/sshbox.exe` | 仓库自带;连接配置见 `tool/sshbox.json`(host/user/pass/port) |
| 远程 Docker | 首次需安装,见第 4 节 |

sshbox 常用命令(所有命令都需带连接参数,或用 `sshbox.json` 配置):

```powershell
# 远程执行命令
tool\sshbox.exe -host <ip> -user <u> -pass <p> run "uname -a"
# 推文件/目录到远程(目标必须写绝对路径!)
tool\sshbox.exe ... push "本地文件或目录" "/home/user/目标目录/"
# 拉文件回本地
tool\sshbox.exe ... pull "/home/user/xxx.tar" "本地目录"
```

### ⚠️ sshbox 的两个坑(本次踩过)

1. **目标路径必须写绝对路径**,写 `~/桌面/...` 不会被展开,文件会落到一个
   名字叫 `~` 的目录里(`/home/user/~/桌面/...`)。
2. **push 带子目录的文件会平铺到目标根目录**,不会保留子目录层级:
   `push api\file_api.go → 根目录/` 时,`file_api.go` 会出现在目标根目录而不是 `api/` 下。
   → 解决:优先整目录 push(如 `push .\docker /home/bury/桌面/OrbitCloud/`),
   或在 push 后用 `mv` 把文件移回正确子目录。

## 2. 同步代码到远程

远程项目位置(中文桌面):`/home/bury/桌面/OrbitCloud`(git 仓库)。

```powershell
# 推送整个 docker 目录(构建必需:entrypoint/templates/nginx 配置)
tool\sshbox.exe ... push "D:\repo\docker" "/home/bury/桌面/OrbitCloud/"

# 推送 Dockerfile / .dockerignore
tool\sshbox.exe ... push "D:\repo\Dockerfile" "/home/bury/桌面/OrbitCloud/"
tool\sshbox.exe ... push "D:\repo\.dockerignore" "/home/bury/桌面/OrbitCloud/"

# 推送变更的源码(先看 git 差异)
git diff --name-only <旧commit> HEAD
# 逐个 push(注意第 1 节的坑:子目录文件会平铺,推完要 mv 归位)
```

## 3. 检查更新(必做)

```powershell
# ① 对比两端 git 提交
git log --oneline -3                                  # 本地最新
tool\sshbox.exe ... run "cd /home/bury/桌面/OrbitCloud && git log --oneline -1"
# ② 若远程落后,从 Windows 推送变更文件(远程 git pull 常因网络不可用)
# ③ 推完后 md5 校验一致性(本次用该方法确认 15 个文件全部一致)
```

> 远程 GitHub/内网源都不可达时,不要指望 git pull,直接 sshbox push 文件最快。

## 4. 准备 Docker(首次)

```bash
# ① 安装
echo '123' | sudo -S apt-get update
echo '123' | sudo -S DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io
echo '123' | sudo -S systemctl enable --now docker

# ② 配置国内镜像加速(直连 docker.io 会超时/无路由)
#   ★ 不要用 heredoc 写 daemon.json(会被转义搞坏),用 base64:
```

```powershell
# Windows 侧生成 base64 再远程解码写入(本次验证可行)
$json = '{"registry-mirrors":["https://docker.m.daocloud.io","https://docker.1ms.run","https://dockerproxy.net"],"ipv6":false}'
$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($json))
tool\sshbox.exe ... run "echo 123 | sudo -S bash -c 'echo $b64 | base64 -d > /etc/docker/daemon.json'; echo 123 | sudo -S systemctl restart docker"
```

## 5. Dockerfile 结构(仓库根)

单镜像多阶段构建,33 步:

| 阶段 | 基础镜像 | 产物 |
|---|---|---|
| backend | `golang:1.26` | `orbitcloud` 后端二进制 |
| gateway | `rust:1.98-slim` | `smb-gateway` SMB 网关 |
| frontend | `node:22` | 前端静态页(`npm ci + build`) |
| runtime | `debian:bookworm-slim` | nginx + 两二进制 + 配置模板 + entrypoint |

镜像内架构:`nginx(80, /api 反代 8080)` + `orbitcloud(8080 + SMB 私有 socket 9101)` + `smb-gateway(445)`。

**两个本次修复的坑(改动已固化在 Dockerfile/entrypoint,勿回退)**:

1. **npm 官方源极慢**(单包下载 600s+):frontend 阶段加了
   `npm config set registry https://registry.npmmirror.com`。
2. **HEALTHCHECK 失效**:`/dev/tcp` 是 bash 语法,但默认用 `/bin/sh`(dash)执行,
   必须写成数组形式指定 bash:
   `CMD ["/usr/bin/bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/8080 && exec 3<>/dev/tcp/127.0.0.1/9101"]`

## 6. 高度定制化配置(postgres 风格)

**不用挂 yaml,直接环境变量,60+ 参数全可配**。原理:entrypoint.sh 启动时对每个参数
做"环境变量优先、未设置用默认值"初始化,再渲染进 yaml。

- 变量命名:`ORBITCLOUD_<配置段>_<字段>`,与 yaml 层级一一对应
  (如 `ORBITCLOUD_LOG_LEVEL`、`ORBITCLOUD_STORAGE_DRIVER`、`ORBITCLOUD_SMB_NETBIOS_NAME`)
- 完整清单见 `docker/docker-compose.prod.yml` 顶部注释
- 生产必改:`ORBITCLOUD_JWT_SECRET`、`ORBITCLOUD_SMB_SHARED_KEY`
- 若改模板:`docker/templates/OrbitCloud.yaml.tpl`(后端)与 `SMB.yaml.tpl`(网关)
  必须配套修改 entrypoint.sh 里的 `def` 默认值
- 多节点/读写分离等超定制场景:挂载自定义 yaml 覆盖
  (`-v ./OrbitCloud.yaml:/opt/orbitcloud/OrbitCloud.yaml`),参考 `docker/templates/*.example`

## 7. 构建镜像(远程)

```powershell
# 必须后台执行:docker build 远超 sshbox 单命令超时
tool\sshbox.exe ... run "echo 123 | sudo -S bash -c 'rm -f /tmp/build.log; cd /home/bury/桌面/OrbitCloud && nohup docker build -t orbitcloud:latest . > /tmp/build.log 2>&1 &'"
# 轮询进度(每次间隔 1-5 分钟,看 Step 走到哪了)
tool\sshbox.exe ... run "grep -E 'Step [0-9]+/[0-9]+' /tmp/build.log | tail -2"
tool\sshbox.exe ... run "tail -2 /tmp/build.log"
# 成功标志
tool\sshbox.exe ... run "grep -E 'Successfully tagged' /tmp/build.log"
```

耗时参考(国内镜像源):Go 阶段几分钟 → Rust cargo 编译 10-20 分钟 → npm 10-20 分钟 → 收尾。

> 改过 Dockerfile/模板后重跑 build,前面阶段走 Docker 缓存会很快,不必重新全量编译。

## 8. 验证(必做)

```bash
# ① 跑起来(注意本机 80 端口可能被占,测试用别的对外端口)
echo '123' | sudo -S docker run -d --name oc-test -p 18080:8080 -p 1445:445 \
  -e ORBITCLOUD_JWT_SECRET=TESTJWT1234567890 \
  -e ORBITCLOUD_SMB_SHARED_KEY=TESTKEY1234567890 \
  -v oc-test-data:/data orbitcloud:latest

# ② 等 30s+,查健康状态(必须 healthy;startup 期间显示 starting)
echo '123' | sudo -S docker inspect --format '{{.State.Health.Status}}' oc-test

# ③ 验证环境变量注入的配置(自定义参数是否生效)
echo '123' | sudo -S docker exec oc-test cat /opt/orbitcloud/OrbitCloud.yaml
echo '123' | sudo -S docker exec oc-test cat /opt/orbitcloud/gateway/SMB.yaml

# ④ 端口连通(容器内 bash /dev/tcp 探测 8080/9101)
echo '123' | sudo -S docker exec oc-test bash -c 'exec 3<>/dev/tcp/127.0.0.1/8080 && echo 8080_OK'

# ⑤ 清理测试
echo '123' | sudo -S docker rm -f oc-test
echo '123' | sudo -S docker volume rm oc-test-data
```

## 9. 导出镜像并拉回 Windows

```bash
# 远程导出(先给 root 读权限,否则 sshbox pull 会 permission denied)
echo '123' | sudo -S sh -c 'docker save -o /home/bury/orbitcloud.tar orbitcloud:latest && chmod 644 /home/bury/orbitcloud.tar'
```

```powershell
# 拉回本地(放到 dist/,已 gitignore)
tool\sshbox.exe ... pull "/home/bury/orbitcloud.tar" "D:\repo\dist"
# 验证
Get-Item "D:\repo\dist\orbitcloud.tar" | Select-Object Length, LastWriteTime
```

## 10. 分发给别人

```bash
# 对方(离线也可):
docker load -i orbitcloud.tar
docker run -d --name orbitcloud -p 80:80 -p 8080:8080 -p 445:445 \
  -v orbitcloud-data:/data \
  -e ORBITCLOUD_JWT_SECRET=xxx -e ORBITCLOUD_SMB_SHARED_KEY=xxx \
  orbitcloud:latest
```

或推送到镜像仓库:`docker tag orbitcloud:latest <仓库>/orbitcloud:latest && docker push <仓库>/orbitcloud:latest`

## 11. 本次踩坑清单(下次直接避免)

| 坑 | 现象 | 解决 |
|---|---|---|
| push 目标写 `~/` | 文件进 `/home/user/~/` 目录 | 写绝对路径 |
| push 子目录文件 | 平铺到目标根,`go build` 报 packages 混在 /src | 整目录 push 或 push 后 mv 归位 |
| 远程 git pull | 超时/网络不可达 | 直接 sshbox push 文件 + md5 校验 |
| 无镜像加速 | `docker pull` no route to host | daemon.json 配 daocloud 等镜像源 |
| heredoc 写 daemon.json | 引号/反斜杠被转义,JSON 损坏,docker 启动失败 | base64 编码写入 |
| envsubst 默认值语法 | `${VAR:-default}` 不被支持,渲染成空 | entrypoint 里用 sh 的 `def()` 预置默认值 |
| npm 官方源 | 单包 600s+,npm ci 卡死 | 配 registry.npmmirror.com |
| HEALTHCHECK 用 /dev/tcp | dash 不认,一直 unhealthy | 数组形式指定 `/usr/bin/bash` |
| sshbox 单命令 60s 超时 | docker build 直接断 | nohup 后台 + 日志轮询 |
| docker save 后权限 | sshbox pull permission denied | chmod 644 |
