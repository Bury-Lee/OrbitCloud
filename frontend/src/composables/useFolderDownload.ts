// useFolderDownload.ts —— 文件夹递归下载管理器(前端主导,替代后端 zip 打包)。
//
// 设计(用户批注):
//   - 文件夹下载:登记后端任务(POST /download-tasks,记录起点文件夹)→ 前端
//     分层拉取目录树(游标分页,每次仅请求当前层级,受 maxDepth 限制)→ 并发
//     逐文件下载(默认 6 并发,worker 池)→ 完成/取消 DELETE 清理任务;
//   - 单文件下载不走本模块(直接 /files/:fid/download 原生下载);
//   - 断点续跑:任务记录(目录树/文件状态/枚举进度)持久化 IndexedDB,刷新页面
//     后可继续;任务行被后端清理(7 天)由前端重新登记,不硬断;
//   - 任务唯一 ID(前端 uuid),支持状态查询(供下载面板展示)。
import { computed, reactive } from 'vue'

import { completeDownloadTask, createDownloadTask, downloadFile, listFilesCursor } from '@/api/files'

/** 全局并发数(建议 5~10) */
export const DOWNLOAD_CONCURRENCY = 6
/** 默认最大递归深度(0 = 只下载当前层文件,不下钻子文件夹) */
export const DOWNLOAD_MAX_DEPTH = 10
/** 游标分页每页条数(与后端 page_size 上限 500 对齐内的稳妥值) */
const PAGE_SIZE = 100
/** 状态落盘节流间隔(ms):文件粒度更新不实时写 IndexedDB */
const PERSIST_THROTTLE = 400

const DB_NAME = 'orbitcloud_downloads'
const DB_VERSION = 1
const STORE = 'tasks'

export type FileDownloadStatus = 'pending' | 'downloading' | 'done' | 'failed'

/** 任务内单个文件的下载状态(目录树扁平化后的叶子节点) */
export interface DownloadFileState {
  fileId: number
  name: string
  /** 相对下载根目录的路径(含文件名),如 "sub/dir/a.txt" */
  relPath: string
  size: number
  status: FileDownloadStatus
  error?: string
}

/** 文件夹下载任务(持久化到 IndexedDB,刷新后可续) */
export interface DownloadTaskState {
  /** 前端唯一任务 ID(uuid) */
  id: string
  /** 后端 download_tasks.id(登记/清理用) */
  serverTaskId: number
  bucketId: number
  /** 下载起点文件夹 folders.id */
  folderId: number
  rootName: string
  /** 起点文件夹在桶内的路径(相对桶根),如 "a/b" */
  rootPath: string
  maxDepth: number
  createdAt: number
  status: 'running' | 'done' | 'failed' | 'cancelled'
  /** 目录树枚举是否完成(决定续跑是否需重走目录树) */
  enumerated: boolean
  /** 文件清单(目录结构树扁平化) */
  files: DownloadFileState[]
  /** 以下为内存运行字段,持久化时剔除 */
  runGen?: number
  abort?: AbortController
}

export interface StartDownloadOptions {
  bucketId: number
  folderId: number
  rootName: string
  rootPath: string
  maxDepth?: number
}

// ---- IndexedDB 存取(任务记录缓存;不可用时降级为仅内存,不影响下载) ----

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      if (!req.result.objectStoreNames.contains(STORE)) {
        req.result.createObjectStore(STORE, { keyPath: 'id' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

async function idbPut(task: DownloadTaskState): Promise<void> {
  try {
    const db = await openDB()
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE, 'readwrite')
      tx.objectStore(STORE).put(stripRuntime(task))
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })
    db.close()
  } catch {
    /* IndexedDB 不可用:仅内存 */
  }
}

async function idbAll(): Promise<DownloadTaskState[]> {
  try {
    const db = await openDB()
    const rows = await new Promise<DownloadTaskState[]>((resolve, reject) => {
      const tx = db.transaction(STORE, 'readonly')
      const req = tx.objectStore(STORE).getAll()
      req.onsuccess = () => resolve(req.result as DownloadTaskState[])
      req.onerror = () => reject(req.error)
    })
    db.close()
    return rows
  } catch {
    return []
  }
}

async function idbDelete(id: string): Promise<void> {
  try {
    const db = await openDB()
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE, 'readwrite')
      tx.objectStore(STORE).delete(id)
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })
    db.close()
  } catch {
    /* 忽略 */
  }
}

/** 持久化前剔除内存运行字段 */
function stripRuntime(task: DownloadTaskState): DownloadTaskState {
  const { runGen: _runGen, abort: _abort, ...rest } = task
  return rest
}

/** 任务内文件去重键(内存态,重启后重建) */
const taskFileKeys = new Map<string, Set<string>>()

function fileKey(fileId: number, relPath: string): string {
  return `${fileId}:${relPath}`
}

function rebuildFileKeys(task: DownloadTaskState) {
  const keys = new Set<string>()
  task.files.forEach((f) => keys.add(fileKey(f.fileId, f.relPath)))
  taskFileKeys.set(task.id, keys)
}

// ---- 响应式任务表(模块级,跨组件共享;IndexedDB 为其持久化镜像) ----

const taskMap = reactive(new Map<string, DownloadTaskState>())
let restored = false

const lastPersistAt = new Map<string, number>()

async function persistNow(task: DownloadTaskState) {
  lastPersistAt.set(task.id, Date.now())
  await idbPut(task)
}

function persistThrottled(task: DownloadTaskState) {
  const now = Date.now()
  if ((lastPersistAt.get(task.id) ?? 0) + PERSIST_THROTTLE > now) return
  lastPersistAt.set(task.id, now)
  void idbPut(task)
}

async function restore() {
  if (restored) return
  restored = true
  for (const t of await idbAll()) {
    t.runGen = 0
    taskMap.set(t.id, t)
    rebuildFileKeys(t)
  }
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

function relOf(dirPath: string, name: string): string {
  return dirPath === '/' ? name : `${dirPath}/${name}`
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// ---- 核心执行 ----

/** 枚举目录树(BFS,每层游标分页翻页;仅取当前层级内容,受 maxDepth 约束) */
async function enumerate(task: DownloadTaskState, queue: DownloadFileState[], gen: number) {
  const stack: { path: string; depth: number }[] = [{ path: task.rootPath, depth: 0 }]
  const visited = new Set<number>() // 已入队子目录(folders.id),防环/防重复
  visited.add(task.folderId)
  while (stack.length > 0 && gen === task.runGen && task.status === 'running') {
    const { path, depth } = stack.shift()!
    let fc = ''
    let dc = ''
    do {
      const res = await listFilesCursor(task.bucketId, {
        path,
        files_cursor: fc,
        folders_cursor: dc,
        page_size: PAGE_SIZE,
      })
      const keys = taskFileKeys.get(task.id)!
      for (const f of res.files) {
        const k = fileKey(f.ID, relOf(path, f.Name))
        if (keys.has(k)) continue // 续跑:已入清单的文件不重复
        keys.add(k)
        const state: DownloadFileState = {
          fileId: f.ID,
          name: f.Name,
          relPath: relOf(path, f.Name),
          size: f.FileSize,
          status: 'pending',
        }
        task.files.push(state)
        queue.push(state)
      }
      if (depth < task.maxDepth) {
        for (const d of res.folders) {
          if (visited.has(d.ID)) continue
          visited.add(d.ID)
          stack.push({ path: relOf(path, d.Name), depth: depth + 1 })
        }
      }
      fc = res.next_files_cursor
      dc = res.next_folders_cursor
      persistThrottled(task)
    } while ((fc !== '' || dc !== '') && gen === task.runGen && task.status === 'running')
  }
  task.enumerated = true
  await persistNow(task)
}

/** 下载单个文件(状态:downloading → done/failed) */
async function downloadOne(task: DownloadTaskState, file: DownloadFileState, gen: number) {
  if (gen !== task.runGen || task.status !== 'running') return
  file.status = 'downloading'
  file.error = undefined
  persistThrottled(task)
  try {
    const blob = await downloadFile(task.bucketId, file.fileId, task.abort?.signal)
    if (gen !== task.runGen || task.status !== 'running') return // 已取消:结果丢弃
    saveBlob(blob, file.name)
    file.status = 'done'
  } catch (e) {
    if (gen !== task.runGen) return
    file.status = 'failed'
    file.error = '下载失败'
    console.warn(`[folder-download] file failed: task=${task.id} ${file.relPath}`, e)
  }
  persistThrottled(task)
}

/** worker 池:从队列取文件逐个下载;队列空且枚举未完成时短暂等待(并发 6) */
async function worker(task: DownloadTaskState, queue: DownloadFileState[], gen: number) {
  while (gen === task.runGen && task.status === 'running') {
    const file = queue.shift()
    if (!file) {
      if (task.enumerated) return // 枚举完成且队列空 → 无更多工作
      await sleep(20)
      continue
    }
    await downloadOne(task, file, gen)
  }
}

async function run(task: DownloadTaskState) {
  task.status = 'running'
  task.abort = new AbortController()
  task.runGen = (task.runGen ?? 0) + 1
  const gen = task.runGen
  // 崩溃残留:进行中的文件复位为待下载
  task.files.forEach((f) => {
    if (f.status === 'downloading') f.status = 'pending'
  })
  const queue = task.files.filter((f) => f.status !== 'done')
  console.info(
    `[folder-download] run: task=${task.id} root=${task.rootPath} maxDepth=${task.maxDepth} ` +
      `resume=${task.files.length > 0 ? `${task.files.length} files cached` : 'fresh'}`,
  )
  await persistNow(task)

  // 枚举与下载并发:枚举不断向队列追加文件,worker 池持续消费
  const enumerateP = enumerate(task, queue, gen)
  const workers = Array.from({ length: DOWNLOAD_CONCURRENCY }, () => worker(task, queue, gen))
  await Promise.all([enumerateP, ...workers])
  if (gen !== task.runGen) return // 已取消/重启

  const failed = task.files.filter((f) => f.status === 'failed').length
  task.status = failed > 0 ? 'failed' : 'done'
  console.info(
    `[folder-download] finish: task=${task.id} status=${task.status} ` +
      `total=${task.files.length} done=${task.files.length - failed - task.files.filter((f) => f.status === 'pending').length} failed=${failed}`,
  )
  await persistNow(task)
  if (task.status === 'done') {
    // 完成清理(尽力而为,失败不阻塞)
    void completeDownloadTask(task.serverTaskId).catch(() => {})
  }
}

// ---- 对外接口 ----

export function useFolderDownload() {
  void restore()

  /** 任务列表(创建时间倒序;含 IndexedDB 恢复的历史任务) */
  const tasks = computed(() => [...taskMap.values()].sort((a, b) => b.createdAt - a.createdAt))

  /** 进行中任务数(供工具栏角标) */
  const runningCount = computed(() => [...taskMap.values()].filter((t) => t.status === 'running').length)

  /** 任务进度统计(已完成/失败/总数;done+failed+其他=total) */
  function progress(t: DownloadTaskState): { done: number; failed: number; total: number } {
    let done = 0
    let failed = 0
    t.files.forEach((f) => {
      if (f.status === 'done') done++
      else if (f.status === 'failed') failed++
    })
    return { done, failed, total: t.files.length }
  }

  /**
   * 开始文件夹下载:登记后端任务 → 枚举 + 并发下载。
   * @throws 登记失败(桶/文件夹不可达、无权限)→ 由调用方提示
   */
  async function startDownload(opts: StartDownloadOptions): Promise<string> {
    const serverTask = await createDownloadTask(opts.bucketId, opts.folderId)
    const id =
      typeof crypto.randomUUID === 'function'
        ? crypto.randomUUID()
        : `${Date.now()}-${Math.random().toString(36).slice(2)}`
    const task: DownloadTaskState = {
      id,
      serverTaskId: serverTask.ID,
      bucketId: opts.bucketId,
      folderId: opts.folderId,
      rootName: opts.rootName,
      rootPath: opts.rootPath,
      maxDepth: opts.maxDepth ?? DOWNLOAD_MAX_DEPTH,
      createdAt: Date.now(),
      status: 'running',
      enumerated: false,
      files: [],
    }
    taskMap.set(id, task)
    taskFileKeys.set(id, new Set())
    console.info(
      `[folder-download] start: task=${id} serverTask=${serverTask.ID} ` +
        `bucket=${opts.bucketId} folder=${opts.folderId} root=${opts.rootPath} maxDepth=${task.maxDepth}`,
    )
    await persistNow(task)
    void run(task)
    return id
  }

  /** 续跑任务(失败/中断后的任务;自动重新登记后端任务,任务行被清理不硬断) */
  async function resumeTask(id: string): Promise<void> {
    const task = taskMap.get(id)
    if (!task || task.status === 'running') return
    try {
      const serverTask = await createDownloadTask(task.bucketId, task.folderId)
      task.serverTaskId = serverTask.ID
    } catch {
      task.status = 'failed'
      return
    }
    void run(task)
  }

  /** 取消任务:中止进行中下载 → 标记 cancelled → 后端清理任务行 */
  async function cancelTask(id: string): Promise<void> {
    const task = taskMap.get(id)
    if (!task || task.status !== 'running') return
    task.runGen = (task.runGen ?? 0) + 1
    task.status = 'cancelled'
    task.abort?.abort()
    console.info(`[folder-download] cancel: task=${id} (${task.rootPath})`)
    await persistNow(task)
    void completeDownloadTask(task.serverTaskId).catch(() => {})
  }

  /** 清除任务记录(仅删本地记录,不再操作后端) */
  async function clearTask(id: string): Promise<void> {
    taskMap.delete(id)
    taskFileKeys.delete(id)
    lastPersistAt.delete(id)
    await idbDelete(id)
  }

  return { tasks, runningCount, progress, startDownload, resumeTask, cancelTask, clearTask }
}
