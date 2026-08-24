// useStructuredUpload.ts —— 「上传所选」结构化复刻(自动创建目录树)
//
// 目标:用户拖入/选择 文件夹+文件 混合内容时,在网盘内自动复刻原目录结构:
//   文件夹 → 按相对路径在目标目录下 mkdir -p 自动创建(后端上传接口 DirPath 自带
//             mkdir -p 建父链,已存在幂等,无需显式 createDir)
//   文件   → 上传到其所属目录(逐文件独立请求,后端单文件接口即 mkdir -p)
//
// 目录树解析(浏览器能力分级):
//   - Chromium 系(Chrome/Edge):DataTransferItem.webkitGetAsEntry() 递归读取,
//     文件携带完整相对路径(支持拖拽文件夹);
//   - 选择文件夹按钮:<input webkitdirectory> 选中的 File.webkitRelativePath 携带相对路径;
//   - 回退(无 entry API):文件平铺上传到当前目录(保持旧行为)。
//
// 上传策略:逐文件顺序独立上传(单文件失败只影响该文件,不中断整批)。
// 进度:字节级回调 (uploadedBytes, totalBytes)——总字节 = Σ文件大小,
// 当前文件用 axios onUploadProgress(loaded/total) 累加,进度条平滑前进。
import { uploadFile } from '@/api/files'

export interface UploadTreeItem {
  /** 相对路径(如 "dir/sub/f.txt";根级文件无 "/" 前缀) */
  relPath: string
  file: File
}

export interface StructuredUploadResult {
  total: number
  ok: number
  failed: { relPath: string; error: string }[]
}

type EntryLike = FileSystemEntry | FileSystemDirectoryEntry | FileSystemFileEntry

/** 递归读取目录条目,产出 {relPath, file} 清单(受 maxDepth 约束防深度循环) */
function readEntry(
  entry: EntryLike,
  relBase: string,
  out: UploadTreeItem[],
  depth: number,
  maxDepth = 50,
): Promise<void> {
  return new Promise((resolve) => {
    if (entry.isFile) {
      ;(entry as FileSystemFileEntry).file(
        (f) => {
          out.push({ relPath: relBase || f.name, file: f })
          resolve()
        },
        () => resolve(),
      )
      return
    }
    if (!entry.isDirectory || depth >= maxDepth) {
      resolve()
      return
    }
    const reader = (entry as FileSystemDirectoryEntry).createReader()
    // readEntries 单次最多返回 100 项,循环取尽(浏览器要求空数组才停止)
    const readBatch = () => {
      reader.readEntries(
        (entries) => {
          if (entries.length === 0) {
            resolve()
            return
          }
          Promise.all(
            entries.map((e) => readEntry(e, relBase ? `${relBase}/${e.name}` : e.name, out, depth + 1, maxDepth)),
          ).then(readBatch)
        },
        () => resolve(),
      )
    }
    readBatch()
  })
}

/** 是否可用目录条目解析(Chromium 系) */
function isEntryApiSupported(): boolean {
  return typeof DataTransferItem !== 'undefined' && 'webkitGetAsEntry' in DataTransferItem.prototype
}

/** 从 DataTransfer 解析文件树(拖拽;文件+文件夹混合)。无 entry API 时平铺返回。 */
export function collectFromDataTransfer(dt: DataTransfer): Promise<UploadTreeItem[]> {
  const items = Array.from(dt.items || [])
  if (!isEntryApiSupported()) {
    const out: UploadTreeItem[] = []
    for (const f of Array.from(dt.files || [])) {
      out.push({ relPath: f.webkitRelativePath || f.name, file: f })
    }
    return Promise.resolve(out)
  }
  const out: UploadTreeItem[] = []
  return Promise.all(
    items
      .map((it) => (it as DataTransferItem & { webkitGetAsEntry?: () => EntryLike | null }).webkitGetAsEntry?.())
      .filter((e): e is EntryLike => !!e)
      .map((e) => readEntry(e, '', out, 0)),
  ).then(() => out)
}

/** 从文件列表解析(选择按钮;webkitdirectory 时 webkitRelativePath 携带结构) */
export function collectFromFiles(files: FileList | File[]): UploadTreeItem[] {
  return Array.from(files).map((f) => ({
    relPath: f.webkitRelativePath || f.name,
    file: f,
  }))
}

/** 清理相对路径为网盘目录路径:去前导分隔符、去文件名的顶层目录段归并 */
function normalizeDirSegments(relPath: string): { dir: string; name: string } {
  const segs = relPath.replace(/\\/g, '/').split('/').filter((s) => s && s !== '.')
  const name = segs.pop() ?? ''
  return { dir: segs.join('/'), name }
}

/**
 * 结构化上传主入口:逐文件独立上传(后端 DirPath 自动 mkdir -p 建父链)。
 * @param bucketId 目标桶
 * @param items 解析后的文件树(相对路径)
 * @param rootDir 网盘目标目录(绝对路径,以 / 开头或空串=桶根)
 * @param onProgress 字节级进度回调(uploadedBytes, totalBytes)
 */
export async function uploadStructured(
  bucketId: number,
  items: UploadTreeItem[],
  rootDir = '/',
  onProgress?: (uploadedBytes: number, totalBytes: number) => void,
): Promise<StructuredUploadResult> {
  const root = rootDir === '/' ? '' : rootDir.replace(/\/+$/, '')
  const totalBytes = items.reduce((sum, it) => sum + it.file.size, 0)

  const total = items.length
  let ok = 0
  let uploadedBytes = 0
  const failed: { relPath: string; error: string }[] = []
  onProgress?.(0, totalBytes)
  // 逐文件顺序上传:单文件失败只记录、继续下一个;目录结构由后端 mkdir -p 自动复刻
  for (const it of items) {
    const { dir, name } = normalizeDirSegments(it.relPath)
    const dirPath = dir ? `${root}/${dir}` : (root || '/')
    const file = new File([it.file], name, { type: it.file.type })
    try {
      await uploadFile(bucketId, file, dirPath, (percent) => {
        onProgress?.(uploadedBytes + Math.round((percent / 100) * file.size), totalBytes)
      })
      ok++
    } catch (e) {
      failed.push({ relPath: it.relPath, error: e instanceof Error ? e.message : '上传失败' })
    }
    uploadedBytes += file.size
    onProgress?.(uploadedBytes, totalBytes)
  }
  return { total, ok, failed }
}
