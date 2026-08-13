// 分享模块 API —— 创建 / 列表 / 修改 / 删除 / 公开解析下载
import axios from 'axios'
import http from './http'
import type { FileItem, FolderItem, ListResult, PageResult, ShareLink } from './types'

/** 公开分享专用 axios 实例(不挂 JWT/401 刷新拦截器:分享是公开端点,无登录态;
 *  且 401 = 提取码错误,不是会话失效,见 router_share.go / server/share_access.go) */
const raw = axios.create({ baseURL: '/api/v1', timeout: 0 })

/** 分享需要提取码(HTTP 401,message "unauthorized") */
export class SharePasswordRequiredError extends Error {
  constructor() {
    super('该分享需要提取码')
    this.name = 'SharePasswordRequiredError'
  }
}

/** 分享不存在 / 已过期 / 次数超限(HTTP 404 / 403) */
export class ShareUnavailableError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ShareUnavailableError'
  }
}

/** 创建分享的入参 */
export interface CreateShareArg {
  /** 被分享条目 ID(文件或文件夹) */
  file_id: number
  /** 权限:仅支持 read(edit 后端不批准,归一为 read) */
  permission?: string
  /** 过期时间(RFC3339;null = 永久) */
  expires_at?: string | null
  /** 下载次数上限(0 = 不限) */
  max_downloads?: number
  /** 提取码(可选) */
  password?: string
}

/** POST /shares 创建分享 */
export function createShare(data: CreateShareArg): Promise<ShareLink> {
  return http.post('/shares', data)
}

/** GET /shares?page=&page_size= 我创建的分享列表(分页) */
export function listShares(params: { page?: number; page_size?: number } = {}): Promise<PageResult<ShareLink>> {
  return http.get('/shares', { params })
}

/** PUT /shares/:id 修改分享(仅传入需要更新的字段) */
export function updateShare(
  id: number,
  data: { permission?: string; expires_at?: string | null; max_downloads?: number; password?: string },
): Promise<ShareLink> {
  return http.put(`/shares/${id}`, data)
}

/** DELETE /shares/:id 删除分享 */
export function deleteShare(id: number): Promise<void> {
  return http.delete(`/shares/${id}`)
}

/** 文件夹分享解析结果(后端:meta=文件夹 + 目录列表) */
export interface ShareFolderResult extends ListResult {
  meta: FolderItem
}

/** 分享解析结果:文件分享 → FileItem;文件夹分享 → ShareFolderResult */
export type ShareResolveResult = FileItem | ShareFolderResult

/** 将 axios 错误归一为分享类型化错误(401 提取码 / 404 失效 / 403 超限) */
function mapShareError(e: unknown): Error {
  const status = (e as { response?: { status?: number } })?.response?.status
  if (status === 401) return new SharePasswordRequiredError()
  if (status === 404) return new ShareUnavailableError('分享不存在或已失效')
  if (status === 403) return new ShareUnavailableError('分享下载次数已达上限')
  return e instanceof Error ? e : new Error('请求失败')
}

/**
 * GET /share/:token?password=&path= 公开解析分享(不计数)。
 * 文件分享 → FileItem;文件夹分享 → {meta, files, folders, total}(path 为分享根内相对路径)。
 * @throws SharePasswordRequiredError 需要提取码 / ShareUnavailableError 失效
 */
export async function resolveShare(
  token: string,
  password = '',
  path = '',
): Promise<ShareResolveResult> {
  try {
    const resp = await raw.get<{ code: number; data?: ShareResolveResult; message: string }>(
      `/share/${token}`,
      { params: { password, path } },
    )
    const body = resp.data
    if (body?.code === 0 && body.data) return body.data
    throw new ShareUnavailableError(body?.message || '分享不可用')
  } catch (e) {
    throw mapShareError(e)
  }
}

/** GET /share/:token?download=1 公开下载分享内容(返回 Blob) */
export async function downloadSharedFile(token: string, password = '', path = ''): Promise<Blob> {
  try {
    const resp = await raw.get(`/share/${token}`, {
      params: { password, path, download: 1 },
      responseType: 'blob',
    })
    return resp.data as Blob
  } catch (e) {
    throw mapShareError(e)
  }
}

/** 组装分享访问链接(前端公开落地页 /share/:token,而非 API 直链) */
export function shareUrl(token: string): string {
  return `${location.origin}/share/${token}`
}

/** 保存 Blob 为本地文件(浏览器触发下载) */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
