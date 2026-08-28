#!/bin/sh
# OrbitCloud 容器入口:渲染配置 → 启动 nginx / smb-gateway / orbitcloud。
#
# 高度定制化:全部配置项均可通过环境变量覆盖(未设置时用内置默认值),
# 用法与 postgres 官方镜像一致——在 docker-compose.yml 的 environment 里
# 只写你要改的项即可, 其余自动取默认值。
# 完整变量清单见: docker/docker-compose.prod.yml 的 environment 注释。
set -e

# ============================================================
# 1. 参数默认值(环境变量优先; 未设置才用默认)
#    变量命名规则: ORBITCLOUD_<配置段>_<字段>, 与 yaml 层级一一对应
# ============================================================
def() {
  _v=$(eval "printf '%s' \"\${$1}\"")
  if [ -z "$_v" ]; then
    eval "export $1='$2'"
  fi
}

# ---- app ----
def ORBITCLOUD_APP_NAME        "orbitcloud"
def ORBITCLOUD_APP_VERSION     "1.4.6"
def ORBITCLOUD_APP_ENVIRONMENT "prod"
def ORBITCLOUD_APP_DEBUG       "false"
def ORBITCLOUD_APP_TIMEZONE     "Asia/Shanghai"

# ---- server ----
def ORBITCLOUD_SERVER_HOST              "0.0.0.0"
def ORBITCLOUD_SERVER_PORT              "8080"
def ORBITCLOUD_SERVER_READ_TIMEOUT      "1"
def ORBITCLOUD_SERVER_WRITE_TIMEOUT     "0"
def ORBITCLOUD_SERVER_IDLE_TIMEOUT      "0"
def ORBITCLOUD_SERVER_MAX_HEADER_BYTES  "1048576"
def ORBITCLOUD_SERVER_MODE              "release"
def ORBITCLOUD_SERVER_PPROF_ENABLED     "false"
def ORBITCLOUD_SERVER_PPROF_DIR         ""

# ---- jwt / refresh_jwt ----
def ORBITCLOUD_JWT_EXPIRE_MINUTES        "720"
def ORBITCLOUD_JWT_ISSUER                "orbitcloud"
def ORBITCLOUD_JWT_ALGORITHM             "HS256"
def ORBITCLOUD_REFRESH_JWT_EXPIRE_MINUTES "10080"
def ORBITCLOUD_REFRESH_JWT_ISSUER        "orbitcloud"
def ORBITCLOUD_REFRESH_JWT_ALGORITHM     "HS256"

# ---- storage ----
def ORBITCLOUD_STORAGE_PROVIDER      "${ORBITCLOUD_STORAGE_PROVIDER:-rustfs}"
def ORBITCLOUD_STORAGE_ENDPOINT      "${STORAGE_ENDPOINT:-http://192.168.1.10:9000}"
def ORBITCLOUD_STORAGE_ACCESS_KEY    "${S3_ACCESS_KEY:-orbitcloud}"
def ORBITCLOUD_STORAGE_SECRET_KEY    "${S3_SECRET_KEY:-orbitcloud123456}"
def ORBITCLOUD_STORAGE_REGION        "us-east-1"
def ORBITCLOUD_STORAGE_USE_SSL       "false"
def ORBITCLOUD_STORAGE_TIMEOUT       "30"
def ORBITCLOUD_STORAGE_MAX_RETRIES   "3"
def ORBITCLOUD_STORAGE_CHUNK_SIZE    "5"
def ORBITCLOUD_STORAGE_MIN_REPLICAS  "2"
def ORBITCLOUD_STORAGE_DRIVER        "${STORAGE_DRIVER:-s3}"
def ORBITCLOUD_STORAGE_REPAIR_INTERVAL     "30s"
def ORBITCLOUD_STORAGE_REPAIR_CONCURRENCY  "4"

# ---- health ----
def ORBITCLOUD_HEALTH_CHECK_INTERVAL "5s"
def ORBITCLOUD_HEALTH_FAIL_THRESHOLD "3"

# ---- pool ----
def ORBITCLOUD_POOL_MAX_WORKERS "64"
def ORBITCLOUD_POOL_QUEUE_SIZE  "1024"

# ---- request_pool ----
def ORBITCLOUD_REQUEST_POOL_MAX_CONCURRENT "256"
def ORBITCLOUD_REQUEST_POOL_QUEUE_SIZE     "512"
def ORBITCLOUD_REQUEST_POOL_STREAMING_MAX  "64"
def ORBITCLOUD_REQUEST_POOL_MODE           "block"

# ---- exec_pool ----
def ORBITCLOUD_EXEC_POOL_MAX_WORKERS   "128"
def ORBITCLOUD_EXEC_POOL_QUEUE_SIZE    "512"
def ORBITCLOUD_EXEC_POOL_STREAM_WORKERS "64"
def ORBITCLOUD_EXEC_POOL_STREAM_QUEUE  "256"

# ---- node(单节点; 多节点请挂载自定义 yaml) ----
def ORBITCLOUD_NODE_ID         "node1"
def ORBITCLOUD_NODE_DRIVER     "${ORBITCLOUD_STORAGE_DRIVER}"
def ORBITCLOUD_NODE_PATH       "${ORBITCLOUD_STORAGE_ENDPOINT}"
def ORBITCLOUD_NODE_ENDPOINT   ""
def ORBITCLOUD_NODE_ACCESS_KEY ""
def ORBITCLOUD_NODE_SECRET_KEY ""
def ORBITCLOUD_NODE_MAX_SPACE  "0"
def ORBITCLOUD_NODE_ENABLED    "true"

# ---- database ----
def ORBITCLOUD_DB_SOURCE         "sqlite"
def ORBITCLOUD_DB_CHARSET        "utf8mb4"
def ORBITCLOUD_DB_LOG_LEVEL      "warn"
def ORBITCLOUD_DB_MAX_IDLE_CONNS "5"
def ORBITCLOUD_DB_MAX_OPEN_CONNS "20"
def ORBITCLOUD_DB_DSN            "${ORBITCLOUD_DB_DSN:-/data/orbitcloud.db}"
def ORBITCLOUD_DB_REPLICA_DSN    ""

# ---- bucket ----
def ORBITCLOUD_BUCKET_ADMIN_CREATE_ONLY "false"

# ---- recycle_bin ----
def ORBITCLOUD_RECYCLE_BIN_RETENTION_DAYS "30"

# ---- rate_limit(限流;skip_paths 为 JSON 数组字符串,如 '["/auth/login"]') ----
def ORBITCLOUD_RATE_LIMIT_ENABLED            "false"
def ORBITCLOUD_RATE_LIMIT_IP_RATE            "20"
def ORBITCLOUD_RATE_LIMIT_IP_BURST           "40"
def ORBITCLOUD_RATE_LIMIT_USER_RATE          "50"
def ORBITCLOUD_RATE_LIMIT_USER_BURST         "100"
def ORBITCLOUD_RATE_LIMIT_SKIP_PATHS         "[]"
def ORBITCLOUD_RATE_LIMIT_RECORD_LIMIT       "1000"
def ORBITCLOUD_RATE_LIMIT_BUCKET_TTL_SECONDS "600"

# ---- smb_gateway(后端侧) ----
def ORBITCLOUD_SMB_GATEWAY_LISTEN_ADDR "127.0.0.1:9101"
def ORBITCLOUD_SMB_CHANNEL_BUFFER      "1024"
def ORBITCLOUD_SMB_MAX_CONCURRENT      "64"

# ---- log(后端) ----
def ORBITCLOUD_LOG_LEVEL        "info"
def ORBITCLOUD_LOG_FORMAT       "text"
def ORBITCLOUD_LOG_OUTPUT_PATH  ""
def ORBITCLOUD_LOG_MAX_SIZE     "0"
def ORBITCLOUD_LOG_MAX_BACKUPS  "0"
def ORBITCLOUD_LOG_MAX_AGE      "0"
def ORBITCLOUD_LOG_DB_LEVEL     ""
def ORBITCLOUD_LOG_RETENTION_DAYS "30"

# ---- smb 网关(smb-gateway 侧) ----
def ORBITCLOUD_SMB_LISTEN                 "0.0.0.0:445"
def ORBITCLOUD_SMB_NETBIOS_NAME           "ORBITCLOUD"
def ORBITCLOUD_SMB_GATEWAY_ADDR           "127.0.0.1:9101"
def ORBITCLOUD_SMB_HEARTBEAT_SECS         "30"
def ORBITCLOUD_SMB_SYNC_INTERVAL_SECS     "60"
def ORBITCLOUD_SMB_RPC_TIMEOUT_SECS       "30"
# 用户快照缓存(大 key 库,认证按需拉取):TTL 秒 / 容量上限
def ORBITCLOUD_SMB_USER_CACHE_TTL_SECS     "60"
def ORBITCLOUD_SMB_USER_CACHE_CAPACITY     "10000"
def ORBITCLOUD_SMB_LOG_LEVEL              "info"
def ORBITCLOUD_SMB_LOG_OUTPUT             "/data/logs"
def ORBITCLOUD_SMB_LOG_DB_ENABLED         "true"
def ORBITCLOUD_SMB_LOG_DB_PATH            "/data/logs/logs.db"
def ORBITCLOUD_SMB_LOG_DB_LEVEL           "info"
def ORBITCLOUD_SMB_LOG_RETENTION_DAYS     "30"
def ORBITCLOUD_SMB_LOG_CLEANUP_INTERVAL_SECS "3600"

# ---- 密钥:未显式提供则随机生成(重启后失效, 生产请固定) ----
if [ -z "$ORBITCLOUD_JWT_SECRET" ]; then
  ORBITCLOUD_JWT_SECRET=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
  echo "[entrypoint] ORBITCLOUD_JWT_SECRET 未设置,已随机生成(重启失效)"
fi
if [ -z "$ORBITCLOUD_SMB_SHARED_KEY" ]; then
  ORBITCLOUD_SMB_SHARED_KEY=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
  echo "[entrypoint] ORBITCLOUD_SMB_SHARED_KEY 未设置,已随机生成(重启失效)"
fi
export ORBITCLOUD_JWT_SECRET ORBITCLOUD_SMB_SHARED_KEY

# ============================================================
# 2. 渲染后端配置(仅首次;已存在则复用,便于挂载卷自定义)
# ============================================================
DATA_DIR=${DATA_DIR:-/data}
mkdir -p "$DATA_DIR/logs" "$DATA_DIR/objects"

cd /opt/orbitcloud
if [ ! -f OrbitCloud.yaml ]; then
  envsubst < templates/OrbitCloud.yaml.tpl > OrbitCloud.yaml
  echo "[entrypoint] 已生成 OrbitCloud.yaml(driver=$ORBITCLOUD_STORAGE_DRIVER)"
fi

# ============================================================
# 3. 渲染 SMB 网关配置(与后端共享密钥保持一致)
# ============================================================
cd /opt/orbitcloud/gateway
if [ ! -f SMB.yaml ]; then
  envsubst < /opt/orbitcloud/templates/SMB.yaml.tpl > SMB.yaml
  echo "[entrypoint] 已生成 SMB.yaml"
fi

# ============================================================
# 4. 启动服务
# ============================================================
# nginx:前端 + /api 反代(失败即退出,便于排查)
nginx -g 'daemon off;' &
# SMB 网关:后台运行(连接后端 127.0.0.1:9101)
/usr/local/bin/smb-gateway &

# 后端前台运行(容器主进程;退出即容器结束)
cd /opt/orbitcloud
exec /usr/local/bin/orbitcloud
