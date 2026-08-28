# ============================================================
# OrbitCloud 单镜像构建(多阶段):后端 Go + SMB 网关 Rust + 前端
#
# 产物:一个镜像内包含
#   - orbitcloud   后端(HTTP API :8080 + SMB 私有 Socket 127.0.0.1:9101)
#   - smb-gateway  SMB 网关(:445,Rust)
#   - nginx        前端静态页(:80)+ /api 反向代理到后端
#   - 内置默认 SQLite + local 对象存储(数据落 /data 卷),开箱即用
#
# 构建:  docker build -t orbitcloud .
# 运行:  docker run -d --name orbitcloud -p 80:80 -p 8080:8080 -p 445:445 \
#           -v orbitcloud-data:/data -e ORBITCLOUD_JWT_SECRET=xxx orbitcloud
# 更多环境变量与用法见文件末尾注释。
# ============================================================

# ---------- 阶段 1:Go 后端 ----------
FROM golang:1.26 AS backend
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY . .
RUN mkdir -p /out && CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags "-s -w" -o /out/orbitcloud .

# ---------- 阶段 2:Rust SMB 网关 ----------
FROM rust:1.98-slim AS gateway
# rusqlite bundled 等依赖需 C 编译器(cc crate)
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY smb_server/rust/ ./rust/
WORKDIR /src/rust
RUN mkdir -p /out && cargo build --release --locked \
    && cp target/release/smb-gateway /out/smb-gateway

# ---------- 阶段 3:前端 ----------
FROM node:22 AS frontend
WORKDIR /src
COPY frontend/package.json frontend/package-lock.json ./
RUN npm config set registry https://registry.npmmirror.com \
    && npm ci
COPY frontend/ .
RUN npm run build

# ---------- 阶段 4:运行时 ----------
FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive \
    TZ=Asia/Shanghai \
    ORBITCLOUD_JWT_SECRET="" \
    ORBITCLOUD_SMB_SHARED_KEY="" \
    STORAGE_DRIVER=local \
    STORAGE_ENDPOINT=/data/objects \
    ORBITCLOUD_DB_DSN=/data/orbitcloud.db
RUN apt-get update && apt-get install -y --no-install-recommends \
        nginx gettext-base ca-certificates tzdata \
    && ln -sf /usr/share/zoneinfo/$TZ /etc/localtime \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /etc/nginx/sites-enabled/default

# 后端 / 网关 / 前端 / 配置模板 / 入口
COPY --from=backend  /out/orbitcloud      /usr/local/bin/orbitcloud
COPY --from=gateway  /out/smb-gateway     /usr/local/bin/smb-gateway
COPY --from=frontend /src/dist            /usr/share/nginx/html
COPY docker/nginx-default.conf            /etc/nginx/conf.d/default.conf
COPY docker/templates/                    /opt/orbitcloud/templates/
COPY docker/entrypoint.sh                 /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && mkdir -p /data /opt/orbitcloud/gateway

WORKDIR /opt/orbitcloud
VOLUME ["/data"]

EXPOSE 80 8080 445
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["/usr/bin/bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/8080 && exec 3<>/dev/tcp/127.0.0.1/9101"]

# ============================================================
# 环境变量说明:
#   ORBITCLOUD_JWT_SECRET      JWT 密钥(不设置则启动时随机生成,重启失效)
#   ORBITCLOUD_SMB_SHARED_KEY  SMB 网关与后端共享密钥(同上,自动生成)
#   STORAGE_DRIVER             local(默认,数据落 /data/objects)| s3
#   STORAGE_ENDPOINT           local: 本地目录; s3: 如 http://host:9000
#   ORBITCLOUD_DB_DSN          SQLite 路径(默认 /data/orbitcloud.db)
#   S3_ACCESS_KEY / S3_SECRET_KEY  s3 驱动时必填
# 生产建议: 固定 JWT/共享密钥并挂载 /data 卷;如需 PostgreSQL,把
#   ORBITCLOUD_DB_DSN 改为 postgres DSN 并调整 templates 中 database 段。
# ============================================================
