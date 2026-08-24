// 文件模块 API —— 列表 / 上传 / 下载 / 预览 / 复制 / 移动 / 删除 / 目录
// 双表模型(后端):文件走 files 表(files/:fid 路由),文件夹走 folders 表(dirs/:fid 路由)。
import axios from 'axios'
import http from './http'
import { TOKEN_KEY } from './http'
import type {
  BatchResult,
  BatchUploadResult,
  CopyItem,
  CursorListResult,
  DownloadTask,
  FileItem,
  FolderItem,
  ListResult,
  MoveItem,
} from './types'

/**
 * 下载 / 预览专用 axios 实例(不挂响应解包拦截器):
 * 后端下载返回二进制流,统一响应拦截器会把 Blob 误当成 ApiResponse 解包成 undefined,
 * 因此这里单独走原始实例,仅注入 Authorization。
 */
const raw = axios.create({ baseURL: '/api/v1', timeout: 0 })
raw.interceptors.request.use((config) => {
  const token = localStorage.getItem(TOKEN_KEY)
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

/**
 * GET /buckets/:id/files?path=&page=&page_size=
 * 列出桶内某目录下的文件与子文件夹(offset 分页,页面浏览;文件与文件夹分数组返回)。
 * @param path 目录路径,缺省桶根 "/";目录不存在时返回 404(浏览语义只读,不自动建目录)
 */
export function listFiles(
  bucketId: number,
  params: { path?: string; page?: number; page_size?: number } = {},
): Promise<ListResult> {
  return http.get(`/buckets/${bucketId}/files`, { params })
}

/**
 * GET /buckets/:id/files?path=&cursor=1&files_cursor=&folders_cursor=&page_size=
 * 游标分页列目录(前端递归下载专用;双列表分别 keyset 游标,不返回 total)。
 * next*_cursor 为空串 = 对应表已取尽;空串回传 = 对应表首页。
 */
export function listFilesCursor(
  bucketId: number,
  params: {
    path: string
    files_cursor?: string
    folders_cursor?: string
    page_size?: number
  },
): Promise<CursorListResult> {
  return http.get(`/buckets/${bucketId}/files`, {
    params: { ...params, cursor: 1 },
  })
}

// ---- 文件夹下载任务(后端 download_tasks;仅文件夹下载登记,进度/断点在前端) ----

/** POST /download-tasks 登记文件夹下载任务(请求体 {bucket_id, folder_id};单文件不登记) */
export function createDownloadTask(bucketId: number, folderId: number): Promise<DownloadTask> {
  return http.post('/download-tasks', { bucket_id: bucketId, folder_id: folderId })
}

/**
 * GET /download-tasks/:id 恢复查询(返回 {task, folder};任务被清理(7 天保留期)→ 404,
 * 由前端重新登记新任务后重发)。
 */
export function getDownloadTask(
  taskId: number,
): Promise<{ task: DownloadTask; folder: FolderItem }> {
  return http.get(`/download-tasks/${taskId}`)
}

/** DELETE /download-tasks/:id 完成/取消后清理任务(硬删,204) */
export function completeDownloadTask(taskId: number): Promise<void> {
  return http.delete(`/download-tasks/${taskId}`)
}

/** GET /buckets/:id/files/:fid 文件元数据(带权限校验) */
export function getFileMeta(bucketId: number, fileId: number): Promise<FileItem> {
  return http.get(`/buckets/${bucketId}/files/${fileId}`)
}

/** GET /buckets/:id/dirs/:fid 文件夹元数据(带权限校验) */
export function getFolderMeta(bucketId: number, dirId: number): Promise<FolderItem> {
  return http.get(`/buckets/${bucketId}/dirs/${dirId}`)
}

/**
 * POST /buckets/:id/files?path= 单文件上传(multipart 字段 file)
 * @param dirPath 目标目录,缺省桶根 "/"
 * @param onProgress 字节级上传进度回调(0~100);timeout: 0 覆盖全局 30s 超时
 * (大文件传输/后端处理可能超过 30s,全局 timeout 会主动掐断请求导致 400 空响应)
 */
export function uploadFile(
  bucketId: number,
  file: File,
  dirPath = '/',
  onProgress?: (percent: number) => void,
): Promise<FileItem> {
  const fd = new FormData()
  fd.append('file', file)
  return http.post(`/buckets/${bucketId}/files`, fd, {
    params: { path: dirPath },
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: 0,
    onUploadProgress: (e) => {
      if (onProgress && e.total) {
        onProgress(Math.round((e.loaded / e.total) * 100))
      }
    },
  })
}

/**
 * POST /buckets/:id/files/batch?path=&folder_id= 批量上传(multipart 字段 files,可多选)
 * 目标目录:dirPath(路径)或 folderId 直传(O(1) 跳过逐段解析,二选一)。
 * 后端逐文件独立处理:部分失败不中断,结果内区分 success / failed。
 */
export function uploadFiles(
  bucketId: number,
  files: File[],
  dirPath = '/',
  folderId?: number,
  onProgress?: (percent: number) => void,
): Promise<BatchUploadResult> {
  const fd = new FormData()
  files.forEach((f) => fd.append('files', f))
  const params: Record<string, string | number> = { path: dirPath }
  if (folderId && folderId > 0) params.folder_id = folderId
  return http.post(`/buckets/${bucketId}/files/batch`, fd, {
    params,
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: 0,
    onUploadProgress: (e) => {
      if (onProgress && e.total) {
        onProgress(Math.round((e.loaded / e.total) * 100))
      }
    },
  })
}

/** GET /buckets/:id/files/:fid/download 下载(返回 Blob;signal 用于取消进行中请求) */
export async function downloadFile(
  bucketId: number,
  fileId: number,
  signal?: AbortSignal,
): Promise<Blob> {
  const resp = await raw.get(`/buckets/${bucketId}/files/${fileId}/download`, {
    responseType: 'blob',
    signal,
  })
  return resp.data as Blob
}

/**
 * GET /buckets/:id/files/:fid/download 流式下载(返回 Response,body 为 ReadableStream)。
 * 供 File System Access 目录落盘边下边写;原生 fetch 走同源 /api 反代,注入 Bearer 头。
 */
export async function downloadFileStream(
  bucketId: number,
  fileId: number,
  signal?: AbortSignal,
): Promise<Response> {
  const token = localStorage.getItem(TOKEN_KEY)
  return fetch(`/api/v1/buckets/${bucketId}/files/${fileId}/download`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    signal,
  })
}

/** GET /buckets/:id/files/:fid/preview 浏览器内预览(inline,返回 Blob) */
export async function previewFile(bucketId: number, fileId: number): Promise<Blob> {
  const resp = await raw.get(`/buckets/${bucketId}/files/${fileId}/preview`, { responseType: 'blob' })
  return resp.data as Blob
}

/**
 * 流媒体播放直链 URL(音视频专用):/buckets/:id/files/:fid/stream?token=xxx。
 * <video>/<audio> 元素发起的请求无法携带 Authorization 头,只能走查询参数;
 * 后端为此单独开了 /stream 接口(QueryTokenAuthMiddleware 查询参数鉴权,
 * 不改变既有下载/预览接口的头部鉴权语义)。接口支持 HTTP Range,浏览器
 * 按需拉取字节区间(206),实现边下边播 + 拖动进度条。
 * 注意:直链仅用于音视频;图片/文档仍走 blob 预览,避免令牌出现在地址栏。
 */
export function previewFileUrl(bucketId: number, fileId: number): string {
  const token = localStorage.getItem(TOKEN_KEY) || ''
  const params = new URLSearchParams()
  if (token) params.set('token', token)
  return `/api/v1/buckets/${bucketId}/files/${fileId}/stream${params.toString() ? `?${params}` : ''}`
}

/** POST /buckets/:id/files/:fid/copy 复制文件(到指定桶/目录/改名) */
export function copyFile(
  bucketId: number,
  fileId: number,
  data: { dst_bucket_id: number; dst_dir?: string; filename?: string },
): Promise<FileItem> {
  return http.post(`/buckets/${bucketId}/files/${fileId}/copy`, data)
}

/** POST /buckets/:id/files/:fid/move 移动/重命名文件(同桶改名/换目录;跨桶 = 剪切) */
export function moveFile(
  bucketId: number,
  fileId: number,
  data: { dst_bucket_id?: number; dst_dir?: string; filename?: string },
): Promise<FileItem> {
  return http.post(`/buckets/${bucketId}/files/${fileId}/move`, data)
}

/** POST /buckets/:id/dirs/:fid/copy 复制文件夹(同/跨桶;目标目录下新建同名目录,子树后台任务完成) */
export function copyFolder(
  bucketId: number,
  dirId: number,
  data: { dst_bucket_id: number; dst_dir?: string },
): Promise<FolderItem> {
  return http.post(`/buckets/${bucketId}/dirs/${dirId}/copy`, data)
}

/** POST /buckets/:id/dirs/:fid/move 移动/重命名文件夹(同桶 O(1);跨桶 MVP 拒绝) */
export function moveFolder(
  bucketId: number,
  dirId: number,
  data: { dst_bucket_id?: number; dst_dir?: string; filename?: string },
): Promise<FolderItem> {
  return http.post(`/buckets/${bucketId}/dirs/${dirId}/move`, data)
}

/** DELETE /buckets/:id/files/:fid 删除文件 */
export function deleteFile(bucketId: number, fileId: number): Promise<void> {
  return http.delete(`/buckets/${bucketId}/files/${fileId}`)
}

/**
 * POST /buckets/:id/dirs 新建文件夹(mkdir -p 语义:父链自动创建,已存在同名文件夹幂等成功)
 * @param path 相对桶根的目录路径,如 "dir/sub"
 */
export function createDir(bucketId: number, path: string): Promise<FolderItem> {
  return http.post(`/buckets/${bucketId}/dirs`, { path })
}

/** DELETE /buckets/:id/dirs/:fid 删除文件夹(逻辑删除 + 后台深度优先物理清理) */
export function deleteDir(bucketId: number, dirId: number): Promise<void> {
  return http.delete(`/buckets/${bucketId}/dirs/${dirId}`)
}

// ---- 批量操作(2026-08-07;行式切片 + 批量级默认值继承,鉴权在 api 层预检) ----

/** POST /buckets/:id/items/batch-delete 批量删除(文件/文件夹混合,逐条独立) */
export function batchDelete(
  bucketId: number,
  items: { kind: 'file' | 'folder'; id: number }[],
): Promise<BatchResult> {
  return http.post(`/buckets/${bucketId}/items/batch-delete`, { items })
}

/** POST /buckets/:id/items/batch-copy 批量复制(同/跨桶;items 内可覆盖目标;src_bucket_id 必传) */
export function batchCopy(
  bucketId: number,
  items: CopyItem[],
  opts: { dst_bucket_id?: number; dst_dir?: string } = {},
): Promise<BatchResult> {
  return http.post(`/buckets/${bucketId}/items/batch-copy`, { ...opts, items })
}

/** POST /buckets/:id/items/batch-move 批量移动/剪切(items 须携带 src_bucket_id;目标桶缺省同桶) */
export function batchMove(
  bucketId: number,
  items: MoveItem[],
  opts: { dst_bucket_id?: number; dst_dir?: string } = {},
): Promise<BatchResult> {
  return http.post(`/buckets/${bucketId}/items/batch-move`, { ...opts, items })
}

/** GET /buckets/:id/items/batch-download 批量下载(zip 流;ids=file:1,folder:2)。
 *  下载/预览同款:走 raw 实例注入 Bearer 头(浏览器直链/新标签页无法携带 Authorization → 401)。 */
export async function batchDownload(
  bucketId: number,
  items: { kind: 'file' | 'folder'; id: number }[],
): Promise<Blob> {
  const ids = items.map((it) => `${it.kind}:${it.id}`).join(',')
  const resp = await raw.get(`/buckets/${bucketId}/items/batch-download`, {
    params: { ids },
    responseType: 'blob',
  })
  return resp.data as Blob
}
