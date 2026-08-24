# OrbitCloud 网页部署工具(Flask)

独立部署工具:浏览器内完成**配置审查**与**自动部署**,无需 SSH 手敲命令。

## 功能

| 模块 | 说明 |
|---|---|
| 环境信息 | 主机/运行时长/内存/磁盘/后端版本/pprof 地址 |
| 服务与端口审查 | orbitcloud / smb-gateway / rustfs / nginx 状态,8080/445/9000/9101/80 监听 |
| 配置审查 | jwt.secret 默认值告警、两侧 shared_key 一致性、网关日志级别、storage.driver 校验 |
| 一键部署 | 完整部署(编译+部署)、仅后端 / 仅 SMB / 仅前端、重启服务,流式日志输出 |
| pprof 入口 | 后端开启 `server.pprof_enabled` 后可直接跳转内存/CPU 画像 |

## 运行

```bash
# 服务器本机(root 运行;默认监听 127.0.0.1:8000)
sudo python3 deploy-tool/app.py

# 指定端口/对外绑定(内网工具,请自行加访问控制)
sudo python3 deploy-tool/app.py --port 8001 --bind 0.0.0.0
```

依赖:`pip install flask`(Ubuntu: `apt install python3-flask`)

浏览器打开 `http://127.0.0.1:8000`。

## 说明

- 部署动作调用仓库 `deploy/deploy.sh`(内部再调 `build/build.sh`、`deploy-backend.sh`、`deploy-smb-gateway.sh`),日志实时回显;
- 单任务串行,重复点击会提示"已有部署任务进行中";
- 工具需以 root 运行或保证 `systemctl`/`ss` 可执行;配置审查只读,不修改任何文件。
