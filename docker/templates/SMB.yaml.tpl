# OrbitCloud SMB 网关容器配置模板(entrypoint 渲染;与后端 smb_gateway.shared_key 一致)
# 全部配置项由环境变量注入(见 entrypoint.sh 的 ORBITCLOUD_SMB_* 默认值清单)
smb:
  listen: "${ORBITCLOUD_SMB_LISTEN}"
  netbios_name: "${ORBITCLOUD_SMB_NETBIOS_NAME}"

gateway:
  addr: "${ORBITCLOUD_SMB_GATEWAY_ADDR}"
  shared_key: "${ORBITCLOUD_SMB_SHARED_KEY}"
  shared_key_env: ""
  heartbeat_secs: ${ORBITCLOUD_SMB_HEARTBEAT_SECS}
  sync_interval_secs: ${ORBITCLOUD_SMB_SYNC_INTERVAL_SECS}
  channel_buffer: ${ORBITCLOUD_SMB_CHANNEL_BUFFER}
  rpc_timeout_secs: ${ORBITCLOUD_SMB_RPC_TIMEOUT_SECS}

log:
  level: "${ORBITCLOUD_SMB_LOG_LEVEL}"
  output: "${ORBITCLOUD_SMB_LOG_OUTPUT}"
  file_prefix: "smb-gateway"
  db_enabled: ${ORBITCLOUD_SMB_LOG_DB_ENABLED}
  db_path: "${ORBITCLOUD_SMB_LOG_DB_PATH}"
  db_level: "${ORBITCLOUD_SMB_LOG_DB_LEVEL}"
  retention_days: ${ORBITCLOUD_SMB_LOG_RETENTION_DAYS}
  cleanup_interval_secs: ${ORBITCLOUD_SMB_LOG_CLEANUP_INTERVAL_SECS}
