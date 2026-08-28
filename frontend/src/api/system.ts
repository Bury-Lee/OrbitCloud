// 系统运维模块 API(仅管理员)—— 运行状况 / 日志文件列表与下载
import axios from 'axios'
import http from './http'
import { TOKEN_KEY } from './http'

/** GET /system/status 运行状况 */
export interface SystemStatus {
  app_name: string
  version: string
  environment: string
  boot_time: string
  uptime_seconds: number
  go_version: string
  num_cpu: number
  num_goroutine: number
  heap_alloc_bytes: number
  db_alive: boolean
  storage_alive: boolean
}

/** 日志文件条目 */
export interface LogFileInfo {
  dir: string
  name: string
  size: number
  mod_time: string
  path: string
  complete: boolean
}

/** GET /system/status 系统运行状况 */
export function getSystemStatus(): Promise<SystemStatus> {
  return http.get('/system/status')
}

/** GET /logs 日志文件列表 */
export function listLogs(): Promise<{ files: LogFileInfo[] }> {
  return http.get('/logs')
}

/**
 * GET /logs/download?path= 下载日志文件(返回 Blob)。
 * 走 raw 实例注入 Bearer 头(浏览器 <a href> 无法携带 Authorization → 401)。
 */
export async function downloadLog(path: string): Promise<Blob> {
  const raw = axios.create({ baseURL: '/api/v1', timeout: 0 })
  raw.interceptors.request.use((config) => {
    const token = localStorage.getItem(TOKEN_KEY)
    if (token) {
      config.headers.Authorization = `Bearer ${token}`
    }
    return config
  })
  const resp = await raw.get('/logs/download', { params: { path }, responseType: 'blob' })
  return resp.data as Blob
}

/** 预览日志内容(拉取文本;超长只取前 N 行由调用方截断) */
export async function previewLog(path: string): Promise<string> {
  const blob = await downloadLog(path)
  return blob.text()
}
