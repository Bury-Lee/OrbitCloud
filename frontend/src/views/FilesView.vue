<script setup lang="ts">
// 文件管理页:元素 = 面包屑 + 工具栏 + 文件表格 + 7 个弹窗(互斥枚举)
// 目录/页码由 URL query 承载(?path=/a/b&page=2):下钻/上级/翻页 = push query,刷新/后退不丢位置
// 操作归属:上传/新建/重命名/移动/复制/分享/可见组/预览 → 弹窗;下载/删除 → 直接动作
import {
  CopyDocument,
  Download,
  Files,
  Folder,
  FolderAdd,
  Refresh,
  Upload,
  UploadFilled,
} from '@element-plus/icons-vue'
import {
  ElMessage,
  ElMessageBox,
} from 'element-plus'
import { computed, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { listBuckets } from '@/api/buckets'
import {
  batchCopy,
  batchDelete,
  batchMove,
  copyFile,
  copyFolder,
  createDir,
  deleteDir,
  deleteFile,
  downloadFile,
  listFiles,
  moveFile,
  moveFolder,
  previewFile,
  previewFileUrl,
} from '@/api/files'
import { listGroups, setFileVisibility, setFolderVisibility } from '@/api/groups'
import { createShare, shareUrl } from '@/api/shares'
import type { Bucket, FileRow, ShareLink, UserGroup } from '@/api/types'
import { formatSize, formatTime, isFolder, toRows } from '@/api/types'
import AppLayout from '@/components/AppLayout.vue'
import FolderTreePicker from '@/components/FolderTreePicker.vue'
import { useDialog } from '@/composables/useDialog'
import { downloadSelected } from '@/composables/useStructuredDownload'
import {
  collectFromDataTransfer,
  collectFromFiles,
  uploadStructured,
  type UploadTreeItem,
} from '@/composables/useStructuredUpload'
import { useFolderDownload } from '@/composables/useFolderDownload'
import { useListState } from '@/composables/useListState'
import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

// ---- 文件夹递归下载(前端主导:分层拉取 + 并发逐文件下载,进度/断点 IndexedDB) ----
const {
  tasks: dlTasks,
  runningCount: dlRunning,
  progress: dlProgress,
  startDownload: dlStart,
  resumeTask: dlResume,
  cancelTask: dlCancel,
  clearTask: dlClear,
} = useFolderDownload()
const dlDialog = ref(false)

async function onDownloadFolder(row: FileRow) {
  try {
    await dlStart({
      bucketId: bucketId.value,
      folderId: row.id,
      rootName: row.name,
      rootPath: joinPath(currentPath.value, row.name),
    })
    ElMessage.success(`已开始下载文件夹「${row.name}」`)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '下载失败')
  }
}

function dlPercent(p: { done: number; total: number }): number {
  return p.total === 0 ? 0 : Math.round((p.done / p.total) * 100)
}

const dlStatusLabel: Record<string, string> = {
  running: '下载中',
  done: '已完成',
  failed: '部分失败',
  cancelled: '已取消',
}
const dlStatusTag: Record<string, 'primary' | 'success' | 'warning' | 'info'> = {
  running: 'primary',
  done: 'success',
  failed: 'warning',
  cancelled: 'info',
}

const bucketId = computed(() => Number(route.params.id))

// ---- 目录状态(URL query 承载;query 即状态,仅 push 变更) ----
const currentPath = computed(() => {
  const p = route.query.path
  const s = Array.isArray(p) ? p[0] : p
  return s ? `/${s.replace(/^\/+/, '')}` : '/'
})

const page = computed(() => {
  const p = route.query.page
  const n = Number(Array.isArray(p) ? p[0] : p)
  return Number.isInteger(n) && n > 0 ? n : 1
})

const pageSize = ref(50)

const breadcrumb = computed(() =>
  currentPath.value === '/' ? [] : currentPath.value.split('/').filter(Boolean),
)

function joinPath(dir: string, name: string): string {
  return dir === '/' ? name : `${dir}/${name}`
}

/** 目录跳转:push query(浏览器后退可回);根目录省略 path 参数 */
function goToPath(path: string) {
  const query: Record<string, string> = {}
  if (path !== '/') query.path = path
  if (page.value > 1) query.page = String(page.value)
  router.push({ name: 'files', params: { id: bucketId.value }, query })
}

function goUp() {
  if (currentPath.value === '/') return
  const segs = breadcrumb.value
  goToPath(segs.length <= 1 ? '/' : segs.slice(0, -1).join('/'))
}

function onPageChange(p: number) {
  const query: Record<string, string> = {}
  if (currentPath.value !== '/') query.path = currentPath.value
  if (p > 1) query.page = String(p)
  router.push({ name: 'files', params: { id: bucketId.value }, query })
}

// ---- 文件列表(Loading → Ready/Error;query 变化即重载) ----
const dialog = useDialog([
  'upload',
  'new-dir',
  'rename',
  'move-copy',
  'share',
  'visibility',
  'preview',
])

// v-model 绑定需直接引用变量(模板不能对函数调用赋值)
const uploadDialog = dialog.model('upload')
const newDirDialog = dialog.model('new-dir')
const renameDialog = dialog.model('rename')
const opDialog = dialog.model('move-copy')
const shareDialog = dialog.model('share')
const visibilityDialog = dialog.model('visibility')
const previewDialog = dialog.model('preview')

const { loading, error, run } = useListState()
const items = ref<FileRow[]>([])
const total = ref(0)

async function load() {
  const res = await run(
    () =>
      listFiles(bucketId.value, {
        path: currentPath.value,
        page: page.value,
        page_size: pageSize.value,
      }),
    (m) => ElMessage.error(m),
  )
  if (res) {
    items.value = toRows(res)
    total.value = res.total
  }
}

// 换桶 → replace 清空 query(回根目录);query 变化会触发下方 watch 加载。
// 边界:旧桶无 query → 新桶无 query 时 replace 是重复导航(resolve failure),
// 不触发 query watch,此时需要兜底 load 一次保证列表刷新。
watch(
  () => route.params.id,
  (newId) => {
    router.replace({ name: 'files', params: { id: newId }, query: {} }).then((failure) => {
      if (failure) load()
    })
  },
)

watch(
  [() => route.query.path, () => route.query.page],
  () => {
    load()
  },
  { immediate: true },
)

function enterDir(row: FileRow) {
  if (!isFolder(row)) return
  goToPath(joinPath(currentPath.value, row.name))
}

// ---- 上传(结构化:复刻文件夹/文件结构,自动创建目录并上传) ----
const uploading = ref(false)
const uploadPercent = ref(0)
const uploadLoaded = ref(0) // 已上传字节数(进度按字节而非文件数计算)
const uploadTotal = ref(0)
const uploadItems = ref<UploadTreeItem[]>([])
const pickFilesInput = ref<HTMLInputElement>()
const pickFolderInput = ref<HTMLInputElement>()

function openUpload() {
  uploadItems.value = []
  uploadPercent.value = 0
  uploadLoaded.value = 0
  uploadTotal.value = 0
  dialog.open('upload')
}

/** 文件选择按钮(可多选文件) */
function onPickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  if (!input.files?.length) return
  uploadItems.value = [...uploadItems.value, ...collectFromFiles(input.files)]
  input.value = ''
}

/** 文件夹选择按钮(webkitdirectory,选中后 webkitRelativePath 携带相对路径) */
function onPickFolder(e: Event) {
  const input = e.target as HTMLInputElement
  if (!input.files?.length) return
  uploadItems.value = [...uploadItems.value, ...collectFromFiles(input.files)]
  input.value = ''
}

/** 拖拽区(drop 时解析目录树;文件/文件夹混合) */
function onDrop(e: DragEvent) {
  if (!e.dataTransfer) return
  e.preventDefault()
  void collectFromDataTransfer(e.dataTransfer).then((items) => {
    if (items.length === 0) {
      ElMessage.warning('未解析到文件,请尝试用「选择文件/文件夹」按钮')
      return
    }
    uploadItems.value = [...uploadItems.value, ...items]
  })
}

function onDragOver(e: DragEvent) {
  e.preventDefault()
}

function removeUploadItem(idx: number) {
  uploadItems.value.splice(idx, 1)
}

async function onUpload() {
  const items = uploadItems.value
  if (items.length === 0) {
    ElMessage.warning('请先选择文件或文件夹')
    return
  }
  uploading.value = true
  uploadPercent.value = 0
  uploadLoaded.value = 0
  uploadTotal.value = 0
  try {
    const res = await uploadStructured(bucketId.value, items, currentPath.value, (uploaded, total) => {
      uploadLoaded.value = uploaded
      uploadTotal.value = total
      uploadPercent.value = total > 0 ? Math.round((uploaded / total) * 100) : 0
    })
    const msg = `上传完成:成功 ${res.ok}/${res.total} 个(目录结构已复刻)`
    if (res.failed.length > 0) {
      ElMessage.warning(
        `${msg},失败 ${res.failed.length} 个(${res.failed.map((f) => f.relPath).join('、')})`,
      )
    } else {
      ElMessage.success(msg)
    }
    dialog.close()
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '上传失败')
  } finally {
    uploading.value = false
  }
}

// ---- 新建文件夹 ----
const newDirName = ref('')
const creatingDir = ref(false)

async function onCreateDir() {
  const name = newDirName.value.trim()
  if (!name) {
    ElMessage.warning('请输入文件夹名称')
    return
  }
  creatingDir.value = true
  try {
    await createDir(bucketId.value, joinPath(currentPath.value, name))
    ElMessage.success('创建成功')
    dialog.close()
    newDirName.value = ''
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '创建失败')
  } finally {
    creatingDir.value = false
  }
}

// ---- 下载 / 预览 ----
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

async function onDownload(row: FileRow) {
  if (isFolder(row)) return
  try {
    const blob = await downloadFile(bucketId.value, row.id)
    saveBlob(blob, row.name)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '下载失败')
  }
}

const previewUrl = ref('')
const previewName = ref('')
const previewType = ref('')
const previewLoading = ref(false)

function isPreviewable(row: FileRow): boolean {
  const t = row.type || ''
  return (
    t.startsWith('image/') ||
    t === 'application/pdf' ||
    t.startsWith('text/') ||
    t.startsWith('video/') ||
    t.startsWith('audio/')
  )
}

function previewKind(): 'image' | 'media' | 'doc' {
  const t = previewType.value
  if (t.startsWith('image/')) return 'image'
  if (t.startsWith('video/') || t.startsWith('audio/')) return 'media'
  return 'doc' // pdf / 文本 / 其他 → iframe
}

function isBlobUrl(u: string): boolean {
  return u.startsWith('blob:')
}

async function onPreview(row: FileRow) {
  if (isFolder(row) || !isPreviewable(row)) {
    ElMessage.info('该类型暂不支持在线预览,请下载后查看')
    return
  }
  previewLoading.value = true
  try {
    const t = row.type || ''
    // 上一份 blob 预览释放;直链(blob: 以外)无需释放
    if (isBlobUrl(previewUrl.value)) {
      URL.revokeObjectURL(previewUrl.value)
    }
    if (t.startsWith('video/') || t.startsWith('audio/')) {
      // 音视频走直链:浏览器对 media 元素自动发 Range 请求(206 分片),
      // 后端 preview 已支持 Range → 边下边播、可拖动进度条,无需整文件缓冲
      previewUrl.value = previewFileUrl(bucketId.value, row.id)
    } else {
      const blob = await previewFile(bucketId.value, row.id)
      previewUrl.value = URL.createObjectURL(blob)
    }
    previewName.value = row.name
    previewType.value = row.type
    dialog.open('preview')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '预览失败')
  } finally {
    previewLoading.value = false
  }
}

// ---- 重命名 ----
const renameTarget = ref<FileRow | null>(null)
const renameName = ref('')
const renaming = ref(false)

function openRename(row: FileRow) {
  renameTarget.value = row
  renameName.value = row.name
  dialog.open('rename')
}

async function onRename() {
  const target = renameTarget.value
  if (!target) return
  const name = renameName.value.trim()
  if (!name) {
    ElMessage.warning('请输入新名称')
    return
  }
  if (name === target.name) {
    dialog.close()
    return
  }
  renaming.value = true
  try {
    if (isFolder(target)) {
      await moveFolder(bucketId.value, target.id, {
        dst_dir: currentPath.value,
        filename: name,
      })
    } else {
      await moveFile(bucketId.value, target.id, { dst_dir: currentPath.value, filename: name })
    }
    ElMessage.success('重命名成功')
    dialog.close()
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '重命名失败')
  } finally {
    renaming.value = false
  }
}

// ---- 移动 / 复制 ----
type OpMode = 'move' | 'copy'
const opMode = ref<OpMode>('move')
const opTarget = ref<FileRow | null>(null)
/** 批量移动/复制待处理条目;非空 = 批量模式(提交后清空) */
const batchPending = ref<FileRow[] | null>(null)
const opBuckets = ref<Bucket[]>([])
const opForm = reactive({ dstBucketId: 0, dstDir: '/' })
const opSubmitting = ref(false)
/** 目录树选择器初始定位:打开弹窗时快照当前所在路径 */
const pickerPath = ref('/')

function onOpPick(picked: { folderId: number; path: string }) {
  opForm.dstDir = picked.path
}

function openOp(mode: OpMode, row: FileRow) {
  opMode.value = mode
  opTarget.value = row
  batchPending.value = null
  opForm.dstBucketId = bucketId.value
  opForm.dstDir = '/'
  pickerPath.value = currentPath.value
  listBuckets()
    .then((bs) => {
      opBuckets.value = bs
    })
    .catch(() => {
      opBuckets.value = []
    })
  dialog.open('move-copy')
}

async function onSubmitOp() {
  const dstDir = opForm.dstDir.trim() || '/'
  opSubmitting.value = true
  try {
    // 批量模式:统一目标,逐条独立(源桶显式携带,后端无缺省回退)
    if (batchPending.value) {
      const items = batchPending.value.map((r) => ({
        kind: isFolder(r) ? ('folder' as const) : ('file' as const),
        id: r.id,
        src_bucket_id: bucketId.value,
      }))
      const res =
        opMode.value === 'move'
          ? await batchMove(bucketId.value, items, { dst_bucket_id: opForm.dstBucketId, dst_dir: dstDir })
          : await batchCopy(bucketId.value, items, { dst_bucket_id: opForm.dstBucketId, dst_dir: dstDir })
      reportBatch(res)
      batchPending.value = null
      dialog.close()
      await load()
      return
    }

    // 单条目模式
    const target = opTarget.value
    if (!target) return
    if (isFolder(target)) {
      if (opMode.value === 'move') {
        await moveFolder(bucketId.value, target.id, {
          dst_bucket_id: opForm.dstBucketId,
          dst_dir: dstDir,
        })
      } else {
        await copyFolder(bucketId.value, target.id, {
          dst_bucket_id: opForm.dstBucketId,
          dst_dir: dstDir,
        })
      }
    } else if (opMode.value === 'move') {
      await moveFile(bucketId.value, target.id, {
        dst_bucket_id: opForm.dstBucketId,
        dst_dir: dstDir,
      })
    } else {
      await copyFile(bucketId.value, target.id, {
        dst_bucket_id: opForm.dstBucketId,
        dst_dir: dstDir,
      })
    }
    ElMessage.success(opMode.value === 'move' ? '移动成功' : '复制成功')
    dialog.close()
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    opSubmitting.value = false
  }
}

// ---- 删除 ----
async function onDelete(row: FileRow) {
  const kind = isFolder(row) ? '文件夹' : '文件'
  try {
    await ElMessageBox.confirm(
      `删除${kind}「${row.name}」?${isFolder(row) ? '文件夹将递归删除全部内容,不可恢复。' : ''}`,
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    if (isFolder(row)) {
      await deleteDir(bucketId.value, row.id)
    } else {
      await deleteFile(bucketId.value, row.id)
    }
    ElMessage.success('删除成功')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

// ---- 分享 ----
const shareTarget = ref<FileRow | null>(null)
const shareSubmitting = ref(false)
const shareResult = ref<ShareLink | null>(null)
const shareForm = reactive({
  maxDownloads: 0,
  expiresDays: 0, // 0 = 永久
  password: '',
})

function openShare(row: FileRow) {
  shareTarget.value = row
  shareResult.value = null
  shareForm.maxDownloads = 0
  shareForm.expiresDays = 0
  shareForm.password = ''
  dialog.open('share')
}

async function onCreateShare() {
  const target = shareTarget.value
  if (!target) return
  shareSubmitting.value = true
  try {
    const expiresAt =
      shareForm.expiresDays > 0
        ? new Date(Date.now() + shareForm.expiresDays * 24 * 3600 * 1000).toISOString()
        : null
    const res = await createShare({
      file_id: target.id,
      max_downloads: shareForm.maxDownloads,
      expires_at: expiresAt,
      password: shareForm.password || undefined,
    })
    shareResult.value = res
    ElMessage.success('分享创建成功')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '创建分享失败')
  } finally {
    shareSubmitting.value = false
  }
}

async function copyShareLink() {
  if (!shareResult.value) return
  const url = `${shareUrl(shareResult.value.Token)}?download=1`
  try {
    await navigator.clipboard.writeText(url)
    ElMessage.success('链接已复制(带提取码访问时浏览器会提示输入)')
  } catch {
    ElMessage.info(`复制失败,请手动复制:${url}`)
  }
}

// ---- 批量操作(多选 + 删除/复制/移动/下载;逐条独立,部分失败提示) ----
const selected = ref<FileRow[]>([])

function selectionChanged(rows: FileRow[]) {
  selected.value = rows
}

async function onBatchDelete() {
  const sel = selected.value
  if (sel.length === 0) {
    ElMessage.warning('请先勾选条目')
    return
  }
  try {
    await ElMessageBox.confirm(
      `删除选中的 ${sel.length} 个条目?${sel.some(isFolder) ? '文件夹将递归删除全部内容,不可恢复。' : ''}`,
      '批量删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  const res = await batchDelete(
    bucketId.value,
    sel.map((r) => ({ kind: isFolder(r) ? 'folder' : 'file', id: r.id })),
  )
  reportBatch(res)
  selected.value = []
  await load()
}

async function onBatchCopy() {
  const sel = selected.value
  if (sel.length === 0) {
    ElMessage.warning('请先勾选条目')
    return
  }
  // 复制对话框复用 openOp 的目标选择逻辑:一次性批量复制到统一目标
  opMode.value = 'copy'
  opTarget.value = null
  opForm.dstBucketId = bucketId.value
  opForm.dstDir = '/'
  pickerPath.value = currentPath.value
  batchPending.value = sel
  try {
    opBuckets.value = await listBuckets()
  } catch {
    opBuckets.value = []
  }
  dialog.open('move-copy')
}

async function onBatchMove() {
  const sel = selected.value
  if (sel.length === 0) {
    ElMessage.warning('请先勾选条目')
    return
  }
  opMode.value = 'move'
  opTarget.value = null
  opForm.dstBucketId = bucketId.value
  opForm.dstDir = '/'
  pickerPath.value = currentPath.value
  batchPending.value = sel
  try {
    opBuckets.value = await listBuckets()
  } catch {
    opBuckets.value = []
  }
  dialog.open('move-copy')
}

/** 批量下载(结构化落盘):自动复现文件目录结构。
 *  Chromium 系浏览器弹系统目录选择框,文件/文件夹按相对路径写入所选目录
 *  (文件夹保留自身名字,内部逐级建子目录);不支持时前端 zip 打包回退。 */
const dlBusy = ref(false)
const dlDone = ref(0)
const dlTotal = ref(0)

async function onDownloadSelected() {
  const sel = selected.value
  if (sel.length === 0) {
    ElMessage.warning('请先勾选条目')
    return
  }
  const entries = sel.map((r) => ({
    kind: (isFolder(r) ? 'folder' : 'file') as 'folder' | 'file',
    id: r.id,
    name: r.name,
    // 文件夹必须带网盘绝对路径(以 / 开头),下载时才能正确枚举子目录
    ...(isFolder(r) ? { path: `/${joinPath(currentPath.value, r.name).replace(/^\/+/, '')}` } : {}),
  }))
  dlBusy.value = true
  dlDone.value = 0
  dlTotal.value = 0
  try {
    const res = await downloadSelected(bucketId.value, entries, (done, total) => {
      dlDone.value = done
      dlTotal.value = total
    })
    if (res.cancelled) return
    const failedNames = res.failed.map((f) => f.relPath).join('、')
    if (res.failed.length > 0) {
      ElMessage.warning(`下载完成 ${res.ok}/${res.total};失败 ${res.failed.length} 个(${failedNames})`)
      return
    }
    if (res.total === 0) {
      ElMessage.warning('所选内容中没有可下载的文件')
      return
    }
    ElMessage.success(
      res.mode === 'dir'
        ? `已下载 ${res.ok} 个文件到所选目录(目录结构已保留)`
        : `已打包 ${res.ok} 个文件为 zip(解压即完整目录结构)`,
    )
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '下载失败')
  } finally {
    dlBusy.value = false
  }
}

/** 批量结果提示(成功 N / 失败 M,失败逐项列名) */
function reportBatch(res: { success: { name?: string }[]; failed: { name?: string; error?: string }[] }) {
  const ok = res.success.length
  const bad = res.failed.length
  if (bad === 0) {
    ElMessage.success(`操作成功:${ok} 项`)
  } else {
    const detail = res.failed.map((f) => `${f.name ?? ''}(${f.error})`).join('、')
    ElMessage.warning(`成功 ${ok} 项,失败 ${bad} 项:${detail}`)
  }
}

// ---- 可见组设置(创建者或管理员) ----
const visibilityTarget = ref<FileRow | null>(null)
const visibilityGroups = ref<number[]>([])
const groupOptions = ref<UserGroup[]>([])
const visibilitySubmitting = ref(false)

/** 是否可设置可见组:创建者或管理员(后端同一权限语义,见 server/visibility.go) */
function canSetVisibility(row: FileRow): boolean {
  return auth.isAdmin || row.uploadedBy === auth.user?.ID
}

async function openVisibility(row: FileRow) {
  visibilityTarget.value = row
  visibilityGroups.value = [...row.visibleGroups]
  try {
    // 管理员看全部组;普通用户后端仅返回自己所在组(见 server/user_group.go ListGroups)
    const res = await listGroups({ page: 1, page_size: 1000 })
    groupOptions.value = res.items
  } catch {
    groupOptions.value = []
  }
  dialog.open('visibility')
}

async function onSaveVisibility() {
  const target = visibilityTarget.value
  if (!target) return
  visibilitySubmitting.value = true
  try {
    if (isFolder(target)) {
      await setFolderVisibility(bucketId.value, target.id, visibilityGroups.value)
    } else {
      await setFileVisibility(bucketId.value, target.id, visibilityGroups.value)
    }
    ElMessage.success('可见组已更新')
    dialog.close()
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '设置失败')
  } finally {
    visibilitySubmitting.value = false
  }
}
</script>

<template>
  <AppLayout>
    <!-- 面包屑 -->
    <div class="crumb-bar">
      <el-breadcrumb separator="/">
        <el-breadcrumb-item>
          <el-link type="primary" :underline="false" @click="goToPath('/')">全部文件</el-link>
        </el-breadcrumb-item>
        <el-breadcrumb-item v-for="(seg, i) in breadcrumb" :key="i">
          <el-link
            type="primary"
            :underline="false"
            @click="goToPath(breadcrumb.slice(0, i + 1).join('/'))"
          >
            {{ seg }}
          </el-link>
        </el-breadcrumb-item>
      </el-breadcrumb>
    </div>

    <!-- 工具栏 -->
    <div class="toolbar">
      <el-button type="primary" :icon="Upload" @click="openUpload">上传文件</el-button>
      <el-button :icon="FolderAdd" @click="dialog.open('new-dir')">新建文件夹</el-button>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="currentPath !== '/'" :icon="Folder" @click="goUp">返回上级</el-button>
      <el-divider direction="vertical" />
      <el-button :disabled="selected.length === 0" type="danger" plain @click="onBatchDelete">
        批量删除
      </el-button>
      <el-button :disabled="selected.length === 0" @click="onBatchCopy">批量复制</el-button>
      <el-button :disabled="selected.length === 0" @click="onBatchMove">批量移动</el-button>
      <el-button
        :disabled="selected.length === 0 || dlBusy"
        :icon="Download"
        :loading="dlBusy"
        @click="onDownloadSelected"
      >
        {{ dlBusy ? `下载中 ${dlDone}/${dlTotal}` : '下载所选' }}
      </el-button>
      <el-button :icon="Download" @click="dlDialog = true">
        下载任务
        <el-badge v-if="dlRunning > 0" :value="dlRunning" class="dl-badge" />
      </el-button>
    </div>

    <div v-if="error" class="error-box">
      <el-result icon="error" :title="`加载失败:${error}`">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>
    </div>

    <!-- 文件表格 -->
    <el-table
      v-else
      v-loading="loading"
      :data="items"
      stripe
      style="width: 100%"
      @row-dblclick="enterDir"
      @selection-change="selectionChanged"
    >
      <el-table-column type="selection" width="45" />
      <el-table-column label="名称" min-width="260">
        <template #default="{ row }">
          <el-icon v-if="isFolder(row)" class="icon folder"><Folder /></el-icon>
          <el-icon v-else class="icon file"><Files /></el-icon>
          <el-link
            v-if="isFolder(row)"
            type="primary"
            :underline="false"
            @click="enterDir(row)"
          >
            {{ row.name }}
          </el-link>
          <span v-else>{{ row.name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="大小" width="120">
        <template #default="{ row }">{{ isFolder(row) ? '—' : formatSize(row.size) }}</template>
      </el-table-column>
      <el-table-column label="类型" width="140">
        <template #default="{ row }">{{ isFolder(row) ? '文件夹' : row.type || '未知' }}</template>
      </el-table-column>
      <el-table-column label="修改时间" width="170">
        <template #default="{ row }">{{ formatTime(row.updatedAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="440" fixed="right">
        <template #default="{ row }">
          <el-button
            v-if="isFolder(row)"
            link
            type="primary"
            size="small"
            @click="onDownloadFolder(row)"
          >
            下载
          </el-button>
          <el-button v-if="!isFolder(row)" link type="primary" size="small" @click="onDownload(row)">
            下载
          </el-button>
          <el-button
            v-if="!isFolder(row)"
            link
            type="primary"
            size="small"
            :loading="previewLoading"
            @click="onPreview(row)"
          >
            预览
          </el-button>
          <el-button link type="primary" size="small" @click="openShare(row)">分享</el-button>
          <el-button
            v-if="canSetVisibility(row)"
            link
            type="primary"
            size="small"
            @click="openVisibility(row)"
          >
            可见组
          </el-button>
          <el-button link type="primary" size="small" @click="openRename(row)">重命名</el-button>
          <el-button link type="primary" size="small" @click="openOp('move', row)">移动</el-button>
          <el-button link type="primary" size="small" @click="openOp('copy', row)">复制</el-button>
          <el-button link type="danger" size="small" @click="onDelete(row)">删除</el-button>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="目录为空 — 上传文件或新建文件夹" :image-size="80" />
      </template>
    </el-table>

    <div class="pager">
      <el-pagination
        :current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        @current-change="onPageChange"
      />
    </div>

    <!-- 下载任务面板(文件夹递归下载:进度/断点在前端 IndexedDB) -->
    <el-dialog v-model="dlDialog" title="下载任务" width="560px">
      <div v-if="dlTasks.length === 0" class="dl-empty">暂无下载任务 — 点击文件夹行的「下载」开始递归下载</div>
      <div v-for="t in dlTasks" :key="t.id" class="dl-task">
        <div class="dl-task-head">
          <span class="dl-task-name">{{ t.rootName }}</span>
          <el-tag size="small" :type="dlStatusTag[t.status] ?? 'info'">
            {{ dlStatusLabel[t.status] ?? t.status }}
          </el-tag>
        </div>
        <el-progress
          :percentage="dlPercent(dlProgress(t))"
          :status="t.status === 'failed' ? 'exception' : t.status === 'done' ? 'success' : undefined"
          :stroke-width="10"
        />
        <div class="dl-task-meta">
          已完成 {{ dlProgress(t).done }}/{{ dlProgress(t).total }}
          <span v-if="dlProgress(t).failed > 0">· 失败 {{ dlProgress(t).failed }}</span>
          <span>· 递归深度 {{ t.maxDepth }}</span>
        </div>
        <div class="dl-task-ops">
          <el-button
            v-if="t.status === 'failed' || t.status === 'cancelled'"
            link
            type="primary"
            size="small"
            @click="dlResume(t.id)"
          >
            继续
          </el-button>
          <el-button v-if="t.status === 'running'" link type="danger" size="small" @click="dlCancel(t.id)">
            取消
          </el-button>
          <el-button link type="info" size="small" @click="dlClear(t.id)">清除记录</el-button>
        </div>
      </div>
    </el-dialog>

    <!-- 上传对话框(结构化:文件/文件夹混合,自动复刻目录结构) -->
    <el-dialog
      v-model="uploadDialog"
      title="上传文件 / 文件夹"
      width="560px"
      :close-on-click-modal="false"
    >
      <div
        class="upload-dropzone"
        @dragover="onDragOver"
        @drop="onDrop"
      >
        <el-icon class="upload-icon"><UploadFilled /></el-icon>
        <div class="el-upload__text">将文件/文件夹拖到此处(自动复刻目录结构)</div>
        <div class="upload-actions">
          <el-button size="small" @click="pickFilesInput?.click()">选择文件</el-button>
          <el-button size="small" @click="pickFolderInput?.click()">选择文件夹</el-button>
        </div>
        <input
          ref="pickFilesInput"
          type="file"
          multiple
          class="upload-hidden-input"
          @change="onPickFiles"
        />
        <input
          ref="pickFolderInput"
          type="file"
          webkitdirectory
          class="upload-hidden-input"
          @change="onPickFolder"
        />
      </div>
      <div v-if="uploadItems.length > 0" class="upload-items">
        <div class="upload-items-head">
          <span>已选 {{ uploadItems.length }} 项</span>
        </div>
        <div class="upload-items-list">
          <div v-for="(it, idx) in uploadItems" :key="`${it.relPath}-${idx}`" class="upload-item">
            <span class="upload-item-name" :title="it.relPath">{{ it.relPath }}</span>
            <span class="upload-item-size">{{ formatSize(it.file.size) }}</span>
            <el-button link type="danger" size="small" @click="removeUploadItem(idx)">移除</el-button>
          </div>
        </div>
      </div>
      <div class="upload-target">上传到目录:{{ currentPath }}</div>
      <el-progress v-if="uploading" :percentage="uploadPercent" :stroke-width="10" class="upload-progress" />
      <div v-if="uploading" class="upload-progress-text">
        已上传 {{ formatSize(uploadLoaded) }} / {{ formatSize(uploadTotal) }}
        <span v-if="uploadTotal > 0">({{ uploadPercent }}%)</span>
      </div>
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="uploading" @click="onUpload">
          {{ uploading ? `上传中 ${uploadPercent}%` : '开始上传' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- 新建文件夹 -->
    <el-dialog v-model="newDirDialog" title="新建文件夹" width="420px">
      <el-input v-model="newDirName" placeholder="文件夹名称" maxlength="255" @keyup.enter="onCreateDir" />
      <div class="form-tip">创建于:{{ currentPath }}</div>
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="creatingDir" @click="onCreateDir">创建</el-button>
      </template>
    </el-dialog>

    <!-- 重命名 -->
    <el-dialog v-model="renameDialog" title="重命名" width="420px">
      <el-input v-model="renameName" maxlength="255" @keyup.enter="onRename" />
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="renaming" @click="onRename">确定</el-button>
      </template>
    </el-dialog>

    <!-- 移动 / 复制(单条/批量共用弹窗,opMode 区分;批量模式 opTarget 为空) -->
    <el-dialog
      v-model="opDialog"
      :title="batchPending ? (opMode === 'move' ? `批量移动(${batchPending.length} 项)` : `批量复制(${batchPending.length} 项)`) : opMode === 'move' ? '移动' : '复制'"
      width="560px"
    >
      <div class="op-bucket">
        <el-select v-model="opForm.dstBucketId" style="width: 100%">
          <el-option
            v-for="b in opBuckets"
            :key="b.ID"
            :value="b.ID"
            :label="b.Name"
          />
        </el-select>
      </div>
      <FolderTreePicker
        :bucket-id="opForm.dstBucketId"
        :current-path="pickerPath"
        @pick="onOpPick"
      />
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="opSubmitting" @click="onSubmitOp">
          {{ opMode === 'move' ? '移动' : '复制' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- 分享(两段式:表单 → 结果) -->
    <el-dialog v-model="shareDialog" title="创建分享" width="480px">
      <template v-if="!shareResult">
        <el-form label-width="90px">
          <el-form-item label="分享对象">
            <span>{{ shareTarget?.name }}</span>
          </el-form-item>
          <el-form-item label="下载上限">
            <el-input-number v-model="shareForm.maxDownloads" :min="0" style="width: 100%" />
            <div class="form-tip">0 = 不限次数</div>
          </el-form-item>
          <el-form-item label="有效期">
            <el-input-number v-model="shareForm.expiresDays" :min="0" :max="365" style="width: 100%" />
            <div class="form-tip">天数;0 = 永久有效</div>
          </el-form-item>
          <el-form-item label="提取码">
            <el-input v-model="shareForm.password" placeholder="可选,留空则无需提取码" maxlength="32" />
          </el-form-item>
        </el-form>
      </template>
      <template v-else>
        <el-result icon="success" title="分享创建成功">
          <template #sub-title>
            <div class="share-result">
              <div>访问链接(直接下载):</div>
              <el-input :model-value="`${shareUrl(shareResult.Token)}?download=1`" readonly />
              <div v-if="shareForm.password" class="form-tip">提取码:{{ shareForm.password }}</div>
              <div class="form-tip">
                有效期:{{ shareResult.ExpiresAt ? formatTime(shareResult.ExpiresAt) : '永久' }} ·
                下载上限:{{ shareResult.MaxDownloads > 0 ? `${shareResult.MaxDownloads} 次` : '不限' }}
              </div>
            </div>
          </template>
          <template #extra>
            <el-button type="primary" :icon="CopyDocument" @click="copyShareLink">复制链接</el-button>
            <el-button @click="dialog.close()">完成</el-button>
          </template>
        </el-result>
      </template>
      <template v-if="!shareResult" #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="shareSubmitting" @click="onCreateShare">创建</el-button>
      </template>
    </el-dialog>

    <!-- 可见组设置 -->
    <el-dialog v-model="visibilityDialog" title="设置可见组" width="480px">
      <el-form label-width="90px">
        <el-form-item label="条目">
          <span>{{ visibilityTarget?.name }}</span>
        </el-form-item>
        <el-form-item label="可见组">
          <el-select v-model="visibilityGroups" multiple filterable clearable style="width: 100%">
            <el-option
              v-for="g in groupOptions"
              :key="g.ID"
              :value="g.ID"
              :label="g.Name"
            />
          </el-select>
          <div class="form-tip">
            选中组后仅组内成员可见(创建者与管理员始终可见);清空/取消全部 = 恢复按桶权限可见
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="visibilitySubmitting" @click="onSaveVisibility">保存</el-button>
      </template>
    </el-dialog>

    <!-- 预览 -->
    <el-dialog v-model="previewDialog" :title="previewName" width="80%" top="5vh">
      <div v-if="previewKind() === 'image'" class="preview-box">
        <img :src="previewUrl" class="preview-img" alt="preview" />
      </div>
      <div v-else-if="previewKind() === 'media'" class="preview-box">
        <video v-if="previewType.startsWith('video/')" :src="previewUrl" controls class="preview-media" />
        <audio v-else :src="previewUrl" controls class="preview-audio" />
      </div>
      <iframe v-else :src="previewUrl" class="preview-frame" />
    </el-dialog>
  </AppLayout>
</template>

<style scoped>
.crumb-bar {
  margin-bottom: 12px;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}

.error-box {
  display: flex;
  justify-content: center;
}

.icon {
  margin-right: 6px;
  vertical-align: -2px;
}
.icon.folder {
  color: #e6a23c;
}
.icon.file {
  color: #909399;
}

.pager {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}

.op-bucket {
  margin-bottom: 10px;
}

.dl-badge {
  margin-left: 6px;
}

.dl-task {
  padding: 10px 0;
  border-bottom: 1px solid #ebeef5;
}

.dl-task:last-child {
  border-bottom: none;
}

.dl-task-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}

.dl-task-name {
  font-weight: 600;
}

.dl-task-meta {
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
}

.dl-task-ops {
  margin-top: 6px;
}

.dl-empty {
  padding: 24px 0;
  text-align: center;
  color: #909399;
}

.upload-icon {
  font-size: 56px;
  color: #c0c4cc;
}

.upload-dropzone {
  border: 1px dashed #dcdfe6;
  border-radius: 6px;
  padding: 24px 12px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  background: #fafafa;
  transition: border-color 0.2s, background 0.2s;
}

.upload-dropzone:hover,
.upload-dropzone.dragover {
  border-color: #409eff;
  background: #f0f7ff;
}

.upload-hidden-input {
  display: none;
}

.upload-actions {
  display: flex;
  gap: 8px;
}

.upload-items {
  margin-top: 10px;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  max-height: 180px;
  overflow-y: auto;
}

.upload-items-head {
  padding: 6px 10px;
  font-size: 12px;
  color: #909399;
  border-bottom: 1px solid #ebeef5;
}

.upload-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  font-size: 13px;
}

.upload-item-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.upload-item-size {
  color: #909399;
  font-size: 12px;
  flex-shrink: 0;
}

.upload-target,
.form-tip {
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
}

.upload-progress-text {
  margin-top: 4px;
  font-size: 12px;
  color: #606266;
  text-align: center;
}

.upload-progress {
  margin-top: 12px;
}

.share-result {
  text-align: left;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.preview-box {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 300px;
  background: #f5f7fa;
}

.preview-img {
  max-width: 100%;
  max-height: 70vh;
  object-fit: contain;
}

.preview-media {
  max-width: 100%;
  max-height: 70vh;
}

.preview-audio {
  width: 80%;
}

.preview-frame {
  width: 100%;
  height: 70vh;
  border: none;
}
</style>
