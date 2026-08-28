# OrbitCloud 容器配置模板(entrypoint 渲染;已存在则不复写,可挂载卷自定义)
# 全部配置项由环境变量注入(见 entrypoint.sh 的 ORBITCLOUD_* 默认值清单)
app:
  name: "${ORBITCLOUD_APP_NAME}"
  version: "${ORBITCLOUD_APP_VERSION}"
  environment: "${ORBITCLOUD_APP_ENVIRONMENT}"
  debug: ${ORBITCLOUD_APP_DEBUG}
  timezone: "${ORBITCLOUD_APP_TIMEZONE}"

server:
  host: "${ORBITCLOUD_SERVER_HOST}"
  port: ${ORBITCLOUD_SERVER_PORT}
  read_timeout: ${ORBITCLOUD_SERVER_READ_TIMEOUT}
  write_timeout: ${ORBITCLOUD_SERVER_WRITE_TIMEOUT}
  idle_timeout: ${ORBITCLOUD_SERVER_IDLE_TIMEOUT}
  max_header_bytes: ${ORBITCLOUD_SERVER_MAX_HEADER_BYTES}
  mode: "${ORBITCLOUD_SERVER_MODE}"
  pprof_enabled: ${ORBITCLOUD_SERVER_PPROF_ENABLED}
  pprof_dir: "${ORBITCLOUD_SERVER_PPROF_DIR}"

jwt:
  secret: "${ORBITCLOUD_JWT_SECRET}"
  expire_minutes: ${ORBITCLOUD_JWT_EXPIRE_MINUTES}
  issuer: "${ORBITCLOUD_JWT_ISSUER}"
  algorithm: "${ORBITCLOUD_JWT_ALGORITHM}"

refresh_jwt:
  expire_minutes: ${ORBITCLOUD_REFRESH_JWT_EXPIRE_MINUTES}
  issuer: "${ORBITCLOUD_REFRESH_JWT_ISSUER}"
  algorithm: "${ORBITCLOUD_REFRESH_JWT_ALGORITHM}"

storage:
  provider: "${ORBITCLOUD_STORAGE_PROVIDER}"
  endpoint: "${ORBITCLOUD_STORAGE_ENDPOINT}"
  access_key: "${ORBITCLOUD_STORAGE_ACCESS_KEY}"
  secret_key: "${ORBITCLOUD_STORAGE_SECRET_KEY}"
  region: "${ORBITCLOUD_STORAGE_REGION}"
  use_ssl: ${ORBITCLOUD_STORAGE_USE_SSL}
  timeout: ${ORBITCLOUD_STORAGE_TIMEOUT}
  max_retries: ${ORBITCLOUD_STORAGE_MAX_RETRIES}
  chunk_size: ${ORBITCLOUD_STORAGE_CHUNK_SIZE}
  min_replicas: ${ORBITCLOUD_STORAGE_MIN_REPLICAS}
  driver: "${ORBITCLOUD_STORAGE_DRIVER}"
  repair_interval: "${ORBITCLOUD_STORAGE_REPAIR_INTERVAL}"
  repair_concurrency: ${ORBITCLOUD_STORAGE_REPAIR_CONCURRENCY}

health:
  check_interval: "${ORBITCLOUD_HEALTH_CHECK_INTERVAL}"
  fail_threshold: ${ORBITCLOUD_HEALTH_FAIL_THRESHOLD}

pool:
  max_workers: ${ORBITCLOUD_POOL_MAX_WORKERS}
  queue_size: ${ORBITCLOUD_POOL_QUEUE_SIZE}

request_pool:
  max_concurrent: ${ORBITCLOUD_REQUEST_POOL_MAX_CONCURRENT}
  queue_size: ${ORBITCLOUD_REQUEST_POOL_QUEUE_SIZE}
  streaming_max: ${ORBITCLOUD_REQUEST_POOL_STREAMING_MAX}
  mode: "${ORBITCLOUD_REQUEST_POOL_MODE}"

exec_pool:
  max_workers: ${ORBITCLOUD_EXEC_POOL_MAX_WORKERS}
  queue_size: ${ORBITCLOUD_EXEC_POOL_QUEUE_SIZE}
  stream_workers: ${ORBITCLOUD_EXEC_POOL_STREAM_WORKERS}
  stream_queue: ${ORBITCLOUD_EXEC_POOL_STREAM_QUEUE}

nodes:
  - id: "${ORBITCLOUD_NODE_ID}"
    driver: "${ORBITCLOUD_NODE_DRIVER}"
    path: "${ORBITCLOUD_NODE_PATH}"
    endpoint: "${ORBITCLOUD_NODE_ENDPOINT}"
    access_key: "${ORBITCLOUD_NODE_ACCESS_KEY}"
    secret_key: "${ORBITCLOUD_NODE_SECRET_KEY}"
    max_space: ${ORBITCLOUD_NODE_MAX_SPACE}
    enabled: ${ORBITCLOUD_NODE_ENABLED}

database:
  source: "${ORBITCLOUD_DB_SOURCE}"
  charset: "${ORBITCLOUD_DB_CHARSET}"
  log_level: "${ORBITCLOUD_DB_LOG_LEVEL}"
  max_idle_conns: ${ORBITCLOUD_DB_MAX_IDLE_CONNS}
  max_open_conns: ${ORBITCLOUD_DB_MAX_OPEN_CONNS}
  sources:
    - dsn: "${ORBITCLOUD_DB_DSN}"
  # replicas 读库列表(sqlite 不支持; 需要时挂载自定义 yaml 配置)

bucket:
  admin_create_only: ${ORBITCLOUD_BUCKET_ADMIN_CREATE_ONLY}

recycle_bin:
  retention_days: ${ORBITCLOUD_RECYCLE_BIN_RETENTION_DAYS}

rate_limit:
  enabled: ${ORBITCLOUD_RATE_LIMIT_ENABLED}
  ip_rate: ${ORBITCLOUD_RATE_LIMIT_IP_RATE}
  ip_burst: ${ORBITCLOUD_RATE_LIMIT_IP_BURST}
  user_rate: ${ORBITCLOUD_RATE_LIMIT_USER_RATE}
  user_burst: ${ORBITCLOUD_RATE_LIMIT_USER_BURST}
  skip_paths: ${ORBITCLOUD_RATE_LIMIT_SKIP_PATHS}
  record_limit: ${ORBITCLOUD_RATE_LIMIT_RECORD_LIMIT}
  bucket_ttl_seconds: ${ORBITCLOUD_RATE_LIMIT_BUCKET_TTL_SECONDS}

smb_gateway:
  listen_addr: "${ORBITCLOUD_SMB_GATEWAY_LISTEN_ADDR}"
  shared_key: "${ORBITCLOUD_SMB_SHARED_KEY}"
  shared_key_env: ""
  channel_buffer: ${ORBITCLOUD_SMB_CHANNEL_BUFFER}
  max_concurrent: ${ORBITCLOUD_SMB_MAX_CONCURRENT}

log:
  level: "${ORBITCLOUD_LOG_LEVEL}"
  format: "${ORBITCLOUD_LOG_FORMAT}"
  output_path: "${ORBITCLOUD_LOG_OUTPUT_PATH}"
  max_size: ${ORBITCLOUD_LOG_MAX_SIZE}
  max_backups: ${ORBITCLOUD_LOG_MAX_BACKUPS}
  max_age: ${ORBITCLOUD_LOG_MAX_AGE}
  db_level: "${ORBITCLOUD_LOG_DB_LEVEL}"
  retention_days: ${ORBITCLOUD_LOG_RETENTION_DAYS}
