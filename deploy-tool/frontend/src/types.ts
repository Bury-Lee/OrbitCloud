// types.ts —— 统一使用 Wails 自动生成的模型类型(wailsjs/go/models)。
// 说明: 避免重复定义导致类型不兼容(convertValues 差异)。
import { model } from '../wailsjs/go/models'

export type ToolConfig = model.ToolConfig
export type GenOpts = model.GenOpts
export type History = model.History
export type AuditResult = model.AuditResult
export type EnvInfo = model.EnvInfo
export type ConfigItem = model.ConfigItem
export type Suggestion = model.Suggestion
export type CheckItem = model.CheckItem
export type GenScriptResult = model.GenScriptResult
export type ScriptConfig = model.ScriptConfig
export type ScriptForm = model.ScriptForm
export type FieldGroup = model.FieldGroup
export type FieldDef = model.FieldDef
export type ValidationIssue = model.ValidationIssue
export type ValidateResult = model.ValidateResult

// 空配置(Go 侧识别为空 = 使用已保存配置)
export const emptyConfig = (): ToolConfig => new model.ToolConfig()

// formToConfig 把表单键值对(map)转换为 ScriptConfig(类型转换)。
// 参数: form Record<string,string> 表单值(checkbox 为 "true"/"false")
// 返回: ScriptConfig
export function formToConfig(form: Record<string, string>): ScriptConfig {
  const c = new model.ScriptConfig()
  const s = (k: string, def: string) => (form[k] !== undefined && form[k] !== '' ? form[k] : def)
  const n = (k: string, def: number) => {
    const v = Number(form[k])
    return Number.isFinite(v) && form[k] !== undefined && form[k] !== '' ? v : def
  }
  const b = (k: string, def: boolean) =>
    form[k] !== undefined && form[k] !== '' ? form[k] === 'true' : def

  c.target = s('target', 'all')
  c.include_sudo = b('include_sudo', true)
  c.app_env = s('app_env', 'prod')
  c.log_level = s('log_level', 'info')
  c.server_host = s('server_host', '0.0.0.0')
  c.server_port = n('server_port', 8080)
  c.pprof_enabled = b('pprof_enabled', false)
  c.deployed_path = s('deployed_path', '/opt/orbitcloud')
  c.repo_path = s('repo_path', '~/桌面/OrbitCloud')
  c.jwt_secret = s('jwt_secret', '')
  c.jwt_expire_minutes = n('jwt_expire_minutes', 720)
  c.superadmin = s('superadmin', 'admin')
  c.superpass = s('superpass', '')
  c.db_source = s('db_source', 'sqlite')
  c.db_dsn = s('db_dsn', 'orbitcloud.db')
  c.storage_driver = s('storage_driver', 's3')
  c.storage_provider = s('storage_provider', 'rustfs')
  c.storage_endpoint = s('storage_endpoint', 'http://127.0.0.1:9000')
  c.storage_access_key = s('storage_access_key', 'rustfsadmin')
  c.storage_secret_key = s('storage_secret_key', 'rustfsadmin')
  c.storage_chunk_size = n('storage_chunk_size', 5)
  c.storage_min_replicas = n('storage_min_replicas', 1)
  c.deploy_rustfs = b('deploy_rustfs', true)
  c.rustfs_path = s('rustfs_path', '/opt/rustfs/data')
  c.rustfs_port = n('rustfs_port', 9000)
  c.node_id = s('node_id', 'node1')
  c.node_path = s('node_path', 'orbitcloud-node1')
  c.node_max_space = n('node_max_space', 0)
  c.smb_listen = s('smb_listen', '0.0.0.0:445')
  c.smb_shared_key = s('smb_shared_key', '')
  c.smb_log_level = s('smb_log_level', 'info')
  c.smb_netbios_name = s('smb_netbios_name', 'ORBITCLOUD')
  c.deploy_frontend = b('deploy_frontend', true)
  c.nginx_port = n('nginx_port', 80)
  c.frontend_dir = s('frontend_dir', '/opt/orbitcloud/web')
  return c
}
