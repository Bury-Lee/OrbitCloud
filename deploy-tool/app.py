#!/usr/bin/env python3
# ============================================================
# OrbitCloud 网页部署工具(Flask)
#
# 功能:
#   1. 自动审查:服务状态 / 端口 / 配置关键项(密钥、日志级别、两侧一致性)
#   2. 自动部署:一键编译部署 / 按组件部署 / 重启服务(流式日志)
#   3. pprof 入口:内存/CPU 画像链接(后端启用 pprof_enabled 时)
#
# 用法(root 运行,监听默认 127.0.0.1:8000):
#   sudo python3 deploy-tool/app.py
#   sudo python3 deploy-tool/app.py --port 8001 --bind 0.0.0.0
#   浏览器打开 http://127.0.0.1:8000
# ============================================================
import argparse
import json
import os
import re
import shutil
import subprocess
import threading
import time
from pathlib import Path

from flask import Flask, jsonify, render_template, request

app = Flask(__name__)

REPO_ROOT = Path(__file__).resolve().parent.parent
OC_HOME = Path("/opt/orbitcloud")
OC_CFG = OC_HOME / "config" / "config.yaml"
GW_CFG = OC_HOME / "smb-gateway" / "config.yaml"

SERVICES = ["orbitcloud", "smb-gateway", "rustfs", "nginx"]
PORTS = {8080: "后端 API", 445: "SMB", 9000: "对象存储", 9101: "网关内部", 80: "前端"}

# ---- 部署任务(单任务串行) ----
_task = {"running": False, "log": "", "exit": None}


def run_cmd(cmd, timeout=1800):
    """执行命令并实时追加日志。"""
    _task["log"] += f"\n$ {' '.join(cmd)}\n"
    try:
        p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                             text=True, encoding="utf-8", errors="replace")
        for line in p.stdout:
            _task["log"] += line
        p.wait(timeout=timeout)
        _task["exit"] = p.returncode
    except Exception as e:  # noqa: BLE001
        _task["log"] += f"\n[工具] 执行失败: {e}\n"
        _task["exit"] = 1
    _task["running"] = False


def deploy(target):
    if _task["running"]:
        return False, "已有部署任务进行中"
    _task.update(running=True, log=f"==> 开始部署目标: {target} [{time.strftime('%H:%M:%S')}]", exit=None)
    if target == "restart":
        cmd = ["systemctl", "restart", "orbitcloud", "smb-gateway"]
    elif target == "backend":
        cmd = ["bash", str(REPO_ROOT / "deploy" / "deploy.sh"), "--skip-build", "--only-backend"]
    elif target == "smb":
        cmd = ["bash", str(REPO_ROOT / "deploy" / "deploy.sh"), "--skip-build", "--only-smb"]
    elif target == "frontend":
        cmd = ["bash", str(REPO_ROOT / "deploy" / "deploy.sh"), "--skip-build", "--only-frontend"]
    else:  # all
        cmd = ["bash", str(REPO_ROOT / "deploy" / "deploy.sh")]
    threading.Thread(target=run_cmd, args=(cmd,), daemon=True).start()
    return True, "部署任务已启动"


def sh(cmd):
    try:
        r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=10)
        return r.stdout.strip()
    except Exception:  # noqa: BLE001
        return ""


def check_config():
    """审查两侧配置关键项,返回问题列表。"""
    issues, items = [], {}

    def yaml_get(path, key, default=""):
        if not path.exists():
            return default
        text = path.read_text(encoding="utf-8", errors="replace")
        m = re.search(rf"^\s*{re.escape(key)}\s*:\s*[\"']?(.*?)[\"']?\s*(?:#.*)?$", text, re.M)
        return m.group(1).strip() if m else default

    # 后端
    jwt = yaml_get(OC_CFG, "secret")
    if "please-change" in jwt:
        issues.append("后端 jwt.secret 仍为默认占位值,建议通过 ORBITCLOUD_JWT_SECRET 注入")
    if not OC_CFG.exists():
        issues.append("后端配置文件缺失: /opt/orbitcloud/config/config.yaml")
    items["后端 jwt.secret"] = "已注入" if "please-change" not in jwt else "默认值(需改)"
    items["后端 smb_gateway.shared_key"] = yaml_get(OC_CFG, "shared_key") or "(未注入)"

    # SMB 网关
    gw_key = yaml_get(GW_CFG, "shared_key")
    items["网关 log.level"] = yaml_get(GW_CFG, "level") or "(未读取)"
    items["网关监听"] = yaml_get(GW_CFG, "listen") or "(未读取)"
    if not GW_CFG.exists():
        issues.append("SMB 网关配置文件缺失: /opt/orbitcloud/smb-gateway/config.yaml")
    elif gw_key and items["后端 smb_gateway.shared_key"] and gw_key != items["后端 smb_gateway.shared_key"]:
        issues.append("两侧 shared_key 不一致(后端/网关),SMB 握手将失败")

    # 日志级别
    if items["网关 log.level"] not in ("debug", "trace"):
        issues.append(f"网关日志级别为 {items['网关 log.level'] or '未知'},调试建议 debug")

    # 存储
    driver = yaml_get(OC_CFG, "driver")
    if driver and driver not in ("s3", "local"):
        issues.append(f"storage.driver 非法: {driver}")
    items["storage.driver"] = driver or "(未读取)"
    return issues, items


@app.route("/")
def index():
    return render_template("index.html")


@app.route("/api/audit")
def audit():
    services = {s: sh(f"systemctl is-active {s} 2>/dev/null") for s in SERVICES}
    ports = {p: bool(sh(f"ss -tln 2>/dev/null | grep -q ':{p} '")) for p in PORTS}
    issues, items = check_config()
    info = {
        "hostname": sh("hostname"),
        "uptime": sh("uptime -p"),
        "disk": sh("df -h /opt | tail -1"),
        "mem": sh("free -h | awk 'NR==2{print $3\"/\"$2}'"),
        "orbitcloud_ver": sh("/opt/orbitcloud/bin/orbitcloud/orbitcloud --version 2>/dev/null || echo 未知"),
        "pprof": sh("ss -tln 2>/dev/null | grep -q ':6060 ' && echo 'http://127.0.0.1:6060/debug/pprof/' || echo 未启用"),
    }
    return jsonify(services=services, ports=ports, issues=issues, items=items, info=info)


@app.route("/api/deploy", methods=["POST"])
def api_deploy():
    target = request.json.get("target", "all")
    if target not in ("all", "backend", "smb", "frontend", "restart"):
        return jsonify(ok=False, msg="非法目标"), 400
    ok, msg = deploy(target)
    return jsonify(ok=ok, msg=msg)


@app.route("/api/deploy/status")
def deploy_status():
    return jsonify(**_task)


def main():
    parser = argparse.ArgumentParser(description="OrbitCloud 网页部署工具")
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--bind", default="127.0.0.1")
    parser.add_argument("--debug", action="store_true")
    args = parser.parse_args()
    print(f"OrbitCloud 部署工具: http://{args.bind}:{args.port}")
    print(f"仓库: {REPO_ROOT}")
    app.run(host=args.bind, port=args.port, debug=args.debug, threaded=True)


if __name__ == "__main__":
    main()
