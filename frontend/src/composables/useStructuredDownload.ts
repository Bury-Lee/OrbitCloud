// useStructuredDownload.ts —— 「下载所选」结构化落盘(自动复现文件目录结构)
//
// 浏览器安全限制:网页无法指定下载目录,<a download> 只能平铺存到默认下载目录,
// 无法复现目录层级。本模块提供两条路:
//   - File System Access API(Chromium 系,Chrome/Edge):
//     用户弹系统目录选择框 → 按相对路径递归创建子目录并流式写入,
//     下载结果即为本地真实目录树(文件夹保持自身名字作为顶层目录);
//   - 回退:不支持该 API 的浏览器 → fflate 前端 zip 打包下载,解压即完整结构。
//
// 流程:文件直接下载写入目标根;文件夹先递归枚举(游标分页,受 maxDepth 限制)
// 生成 {relPath, fileId} 清单,再逐文件写入/入包。失败逐项记录,不中断整体。
import { Zip, ZipPassThrough } from 'fflate'

import { downloadFile, downloadFileStream, listFilesCursor } from '@/api/files'

export interface SelectEntry {
  kind: 'file' | 'folder'
  id: number
  name: string
  /** 条目在网盘中的绝对路径(文件夹 = 自身完整路径,如 /test/测试;文件无需)。
   *  枚举文件夹必须传,否则后端按绝对路径解析会 404,文件夹内容静默丢失。 */
  path?: string
}

interface CollectFile {
  fileId: number
  relPath: string
  size: number
}

export interface StructuredResult {
  /** true = 用户在系统目录选择框点了取消 */
  cancelled: boolean
  mode: 'dir' | 'zip'
  total: number
  ok: number
  failed: { relPath: string }[]
}

export const MAX_DEPTH = 10
const PAGE_SIZE = 100

/** showDirectoryPicker 不在当前 TS lib.dom 类型中,显式声明(运行时已在调用前判存在) */
type DirectoryPicker = (opts?: { id?: string; mode?: 'readwrite' | 'read' }) => Promise<FileSystemDirectoryHandle>

function pickDirectory(): Promise<FileSystemDirectoryHandle> {
  const picker = (window as unknown as { showDirectoryPicker?: DirectoryPicker }).showDirectoryPicker
  if (!picker) throw new Error('unsupported')
  return picker({ id: 'orbitcloud-download', mode: 'readwrite' })
}

function joinPath(dir: string, name: string): string {
  return dir === '/' ? name : `${dir}/${name}`
}

function isFsAccessSupported(): boolean {
  return typeof window !== 'undefined' && 'showDirectoryPicker' in window
}

/** 递归枚举文件夹(游标分页取尽,受 maxDepth 约束),生成相对路径文件清单。
 *  参数:apiPath = 文件夹的网盘绝对路径(调 API,以 / 开头);
 *       relRoot = 落盘相对根(文件夹自身名,不含祖先路径)——保持"文件夹保留自身名字"语义,
 *       无论从哪个层级选中,下载后顶层目录都只有该文件夹本身。
 *  栈内维护 apiPath(带前导 /,调 API)与 relPath(相对,用于目标目录/zip 条目)。 */
async function collectFolder(
  bucketId: number,
  folderId: number,
  apiPath: string,
  relRoot: string,
  maxDepth: number,
): Promise<CollectFile[]> {
  const out: CollectFile[] = []
  const stack: { apiPath: string; relPath: string; depth: number }[] = [
    { apiPath, relPath: relRoot, depth: 0 },
  ]
  const visited = new Set<number>()
  visited.add(folderId)
  while (stack.length > 0) {
    const { apiPath: ap, relPath, depth } = stack.shift()!
    let fc = ''
    let dc = ''
    do {
      const res = await listFilesCursor(bucketId, {
        path: ap,
        files_cursor: fc,
        folders_cursor: dc,
        page_size: PAGE_SIZE,
      })
      for (const f of res.files) {
        out.push({ fileId: f.ID, relPath: joinPath(relPath, f.Name), size: f.FileSize })
      }
      if (depth < maxDepth) {
        for (const d of res.folders) {
          if (visited.has(d.ID)) continue
          visited.add(d.ID)
          stack.push({ apiPath: joinPath(ap, d.Name), relPath: joinPath(relPath, d.Name), depth: depth + 1 })
        }
      }
      fc = res.next_files_cursor
      dc = res.next_folders_cursor
    } while (fc !== '' || dc !== '')
  }
  return out
}

/** 按相对路径流式写入目录句柄(逐级创建子目录,边下边写不占内存,覆盖同名文件) */
async function writeStreamToDir(
  root: FileSystemDirectoryHandle,
  relPath: string,
  resp: Response,
  onBytes?: (received: number, total: number) => void,
): Promise<void> {
  if (!resp.ok || !resp.body) {
    throw new Error(`HTTP ${resp.status}`)
  }
  const parts = relPath.split('/')
  let dir = root
  for (let i = 0; i < parts.length - 1; i++) {
    dir = await dir.getDirectoryHandle(parts[i], { create: true })
  }
  const fh = await dir.getFileHandle(parts[parts.length - 1], { create: true })
  const w = await fh.createWritable()
  try {
    const reader = resp.body.getReader()
    const writer = w.getWriter()
    let received = 0
    const total = Number(resp.headers.get('Content-Length') ?? 0)
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      await writer.write(value)
      received += value.length
      onBytes?.(received, total)
    }
    await writer.close()
  } finally {
    // FileSystemWritableFileStream 继承 WritableStream,abort 兜底(已 close 时无副作用)
    await w.abort().catch(() => {})
  }
}

/** 目录落盘模式:所有条目流式写入用户选择的目录(文件夹按相对路径建子目录) */
async function downloadToDirectory(
  bucketId: number,
  entries: SelectEntry[],
  onProgress?: (done: number, total: number) => void,
): Promise<StructuredResult> {
  const root = await pickDirectory()
  // 构建全量清单:文件 → 根下;文件夹 → rootName/... 相对路径
  const files: { fileId: number; relPath: string }[] = []
  const failed: { relPath: string }[] = []
  for (const e of entries) {
    if (e.kind === 'file') {
      files.push({ fileId: e.id, relPath: e.name })
    } else {
      try {
        // apiPath = 文件夹的网盘绝对路径(缺省兜底为自身名),relRoot = 文件夹自身名
        const apiPath = e.path || `/${e.name.replace(/^\/+/, '')}`
        const list = await collectFolder(bucketId, e.id, apiPath, e.name, MAX_DEPTH)
        files.push(...list.map((f) => ({ fileId: f.fileId, relPath: f.relPath })))
      } catch {
        failed.push({ relPath: e.name })
      }
    }
  }
  const total = files.length
  let ok = 0
  onProgress?.(0, total)
  for (const f of files) {
    try {
      const resp = await downloadFileStream(bucketId, f.fileId)
      await writeStreamToDir(root, f.relPath, resp)
      ok++
    } catch {
      failed.push({ relPath: f.relPath })
    }
    onProgress?.(ok, total)
  }
  return { cancelled: false, mode: 'dir', total, ok, failed }
}

/** 回退:fflate 前端 zip 打包(保留相对路径,解压即完整结构)。
 *  流式实现:ZipPassThrough 存储级(不压缩,网盘文件多为已压缩内容,压缩纯耗 CPU)
 *  逐文件入包,下载与打包流水线并行——进度连续,无"下载完再组装"的等待段。 */
async function downloadAsZip(
  bucketId: number,
  entries: SelectEntry[],
  onProgress?: (done: number, total: number) => void,
): Promise<StructuredResult> {
  const files: { fileId: number; relPath: string }[] = []
  const failed: { relPath: string }[] = []
  for (const e of entries) {
    if (e.kind === 'file') {
      files.push({ fileId: e.id, relPath: e.name })
    } else {
      try {
        const apiPath = e.path || `/${e.name.replace(/^\/+/, '')}`
        const list = await collectFolder(bucketId, e.id, apiPath, e.name, MAX_DEPTH)
        files.push(...list.map((f) => ({ fileId: f.fileId, relPath: f.relPath })))
      } catch {
        failed.push({ relPath: e.name })
      }
    }
  }
  const total = files.length
  if (total === 0) {
    return { cancelled: false, mode: 'zip', total, ok: 0, failed }
  }
  const chunks: Uint8Array[] = []
  let zipErr: Error | null = null
  const zipStream = new Zip((err, data) => {
    if (err) {
      zipErr = err
      return
    }
    chunks.push(data)
  })
  let ok = 0
  onProgress?.(0, total)
  for (const f of files) {
    try {
      const blob = await downloadFile(bucketId, f.fileId)
      const buf = new Uint8Array(await blob.arrayBuffer())
      const file = new ZipPassThrough(f.relPath)
      zipStream.add(file)
      file.push(buf, true) // 存储级:立即输出该文件条目,压缩耗时≈0
      ok++
    } catch {
      failed.push({ relPath: f.relPath })
    }
    onProgress?.(ok, total)
  }
  zipStream.end()
  if (zipErr) {
    throw zipErr
  }
  const size = chunks.reduce((n, c) => n + c.length, 0)
  const zipped = new Uint8Array(size)
  let off = 0
  for (const c of chunks) {
    zipped.set(c, off)
    off += c.length
  }
  const url = URL.createObjectURL(new Blob([zipped], { type: 'application/zip' }))
  const a = document.createElement('a')
  a.href = url
  a.download = '下载所选.zip'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  return { cancelled: false, mode: 'zip', total, ok, failed }
}

/**
 * 「下载所选」主入口:优先目录落盘(File System Access),不支持时 zip 回退。
 * 用户取消目录选择 → {cancelled: true}(调用方静默处理)。
 */
export async function downloadSelected(
  bucketId: number,
  entries: SelectEntry[],
  onProgress?: (done: number, total: number) => void,
): Promise<StructuredResult> {
  if (isFsAccessSupported()) {
    try {
      return await downloadToDirectory(bucketId, entries, onProgress)
    } catch (e) {
      // 用户取消目录选择(AbortError)→ 不弹错误
      if (e instanceof DOMException && e.name === 'AbortError') {
        return { cancelled: true, mode: 'dir', total: 0, ok: 0, failed: [] }
      }
      // 目录权限/磁盘异常 → 回退 zip,保证功能可用
      console.warn('[structured-download] dir write failed, fallback to zip', e)
    }
  }
  return downloadAsZip(bucketId, entries, onProgress)
}
