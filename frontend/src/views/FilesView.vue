<script setup lang="ts">
// 文件管理主视图:面包屑 / 分页 / 列表与各操作弹窗组合。
// 弹窗与表格已拆至 components/files/,本文件只保留页面编排与少量业务逻辑。
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'

import { listBuckets } from '@/api/buckets'
import { batchDelete, deleteDir, deleteFile, downloadFile } from '@/api/files'
import { isFolder, toRows, type FileRow } from '@/api/types'
import { isPreviewable } from '@/utils/preview'
import { Search } from '@element-plus/icons-vue'
import AppLayout from '@/components/AppLayout.vue'
import DownloadTasksDialog from '@/components/files/DownloadTasksDialog.vue'
import FileListTable from '@/components/files/FileListTable.vue'
import FilesToolbar from '@/components/files/FilesToolbar.vue'
import MoveCopyDialog from '@/components/files/MoveCopyDialog.vue'
import NewDirDialog from '@/components/files/NewDirDialog.vue'
import PreviewDialog from '@/components/files/PreviewDialog.vue'
import RenameDialog from '@/components/files/RenameDialog.vue'
import ShareDialog from '@/components/files/ShareDialog.vue'
import UploadDialog from '@/components/files/UploadDialog.vue'
import VisibilityDialog from '@/components/files/VisibilityDialog.vue'
import { downloadSelected } from '@/composables/useStructuredDownload'
import { useFolderDownload } from '@/composables/useFolderDownload'
import { useListState } from '@/composables/useListState'
import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

// ---- 路径与分页 ----
const bucketId = computed(() => Number(route.params.id))
const currentPath = computed(() => {
  const p = route.query.path
  return typeof p === 'string' && p ? p : '/'
})
const breadcrumb = computed(() => currentPath.value.split('/').filter(Boolean))
const page = computed(() => {
  const p = Number(route.query.page)
  return Number.isFinite(p) && p >= 1 ? p : 1
})
const pageSize = ref(50)
const keyword = ref('')
const order = ref('desc') // 排序方向(字段固定为创建时间;仅 asc/desc)

function joinPath(dir: string, name: string): string {
  return dir === '/' ? `/${name}` : `${dir}/${name}`
}

function goToPath(path: string) {
  router.push({ path: `/buckets/${bucketId.value}`, query: { path: path === '/' ? undefined : path, page: undefined } })
}

function goUp() {
  const segs = currentPath.value.split('/').filter(Boolean)
  goToPath(segs.length <= 1 ? '/' : segs.slice(0, -1).join('/'))
}

function enterDir(row: FileRow) {
  goToPath(joinPath(currentPath.value, row.name))
}

function onPageChange(p: number) {
  router.push({
    path: `/buckets/${bucketId.value}`,
    query: { path: currentPath.value === '/' ? undefined : currentPath.value, page: p > 1 ? p : undefined },
  })
}

// ---- 列表加载 ----
const { loading, error, run } = useListState()
const items = ref<FileRow[]>([])
const total = ref(0)

async function load() {
  const rows = await run(async () => {
    const res = await listBuckets()
    const b = res.find((x) => x.ID === bucketId.value)
    if (!b) throw new Error('桶不存在')
    const list = await import('@/api/files').then((m) =>
      m.listFiles(bucketId.value, {
        path: currentPath.value,
        page: page.value,
        page_size: pageSize.value,
        key: keyword.value || undefined,
        order: order.value,
      }),
    )
    return { rows: toRows(list), total: list.total }
  })
  if (rows) {
    items.value = rows.rows
    total.value = rows.total
  }
}

// 搜索/排序变化:重置到第一页后加载(page 为路由派生只读,经 goToPath 重置)
let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearchInput() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    goToPath(currentPath.value)
    load()
  }, 300)
}

function onOrderChange() {
  goToPath(currentPath.value)
  load()
}

watch([bucketId, currentPath, page], load, { immediate: true })

// ---- 弹窗开关 ----
const uploadOpen = ref(false)
const newDirOpen = ref(false)
const renameOpen = ref(false)
const opOpen = ref(false)
const shareOpen = ref(false)
const visibilityOpen = ref(false)
const previewOpen = ref(false)
const dlOpen = ref(false)

// ---- 下载(单文件 / 文件夹 / 批量) ----
const { tasks, runningCount, progress, startDownload, resumeTask, cancelTask, clearTask } =
  useFolderDownload()

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

async function onDownloadFolder(row: FileRow) {
  if (!isFolder(row)) return
  try {
    await startDownload({
      bucketId: bucketId.value,
      folderId: row.id,
      rootName: row.name,
      rootPath: joinPath(currentPath.value, row.name),
    })
    ElMessage.success(`已开始下载文件夹「${row.name}」`)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '下载失败')
  }
  dlOpen.value = true
}

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

// ---- 删除(单条 / 批量) ----
async function onDelete(row: FileRow) {
  const kind = isFolder(row) ? '文件夹' : '文件'
  try {
    await ElMessageBox.confirm(
      `删除${kind}「${row.name}」?${isFolder(row) ? '文件夹将递归删除全部内容。' : ''}`,
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
    ElMessage.success('已移入回收站')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

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
      `删除选中的 ${sel.length} 个条目?${sel.some(isFolder) ? '文件夹将递归删除全部内容。' : ''}`,
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
  const ok = res.success.length
  const bad = res.failed.length
  if (bad === 0) {
    ElMessage.success(`操作成功:${ok} 项(已移入回收站)`)
  } else {
    const detail = res.failed.map((f) => `${f.name ?? ''}(${f.error})`).join('、')
    ElMessage.warning(`成功 ${ok} 项,失败 ${bad} 项:${detail}`)
  }
  selected.value = []
  await load()
}

// ---- 移动 / 复制(单条 / 批量共用弹窗) ----
type OpMode = 'move' | 'copy'
const opMode = ref<OpMode>('move')
const opTarget = ref<FileRow | null>(null)
const batchPending = ref<FileRow[] | null>(null)

function openOp(mode: OpMode, row: FileRow) {
  opMode.value = mode
  opTarget.value = row
  batchPending.value = null
  opOpen.value = true
}

async function onBatchCopy() {
  const sel = selected.value
  if (sel.length === 0) {
    ElMessage.warning('请先勾选条目')
    return
  }
  opMode.value = 'copy'
  opTarget.value = null
  batchPending.value = sel
  opOpen.value = true
}

async function onBatchMove() {
  const sel = selected.value
  if (sel.length === 0) {
    ElMessage.warning('请先勾选条目')
    return
  }
  opMode.value = 'move'
  opTarget.value = null
  batchPending.value = sel
  opOpen.value = true
}

// ---- 分享 / 可见组 / 预览 / 重命名 ----
const shareTarget = ref<FileRow | null>(null)
const visibilityTarget = ref<FileRow | null>(null)
const previewTarget = ref<FileRow | null>(null)
const renameTarget = ref<FileRow | null>(null)

function openRename(row: FileRow) {
  renameTarget.value = row
  renameOpen.value = true
}

function openShare(row: FileRow) {
  shareTarget.value = row
  shareOpen.value = true
}

function openVisibility(row: FileRow) {
  visibilityTarget.value = row
  visibilityOpen.value = true
}

function canSetVisibility(row: FileRow): boolean {
  return auth.isAdmin || row.uploadedBy === auth.user?.ID
}

function openPreview(row: FileRow) {
  if (isFolder(row) || !isPreviewable(row.type || '', row.name)) {
    ElMessage.info('该类型暂不支持在线预览,请下载后查看')
    return
  }
  previewTarget.value = row
  previewOpen.value = true
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
          <el-link type="primary" :underline="false" @click="goToPath(breadcrumb.slice(0, i + 1).join('/'))">
            {{ seg }}
          </el-link>
        </el-breadcrumb-item>
      </el-breadcrumb>
    </div>

    <div class="toolbar-row">
      <el-input
        v-model="keyword"
        placeholder="搜索文件名…"
        clearable
        style="width: 220px"
        @input="onSearchInput"
        @clear="onSearchInput"
      >
        <template #prefix><el-icon><Search /></el-icon></template>
      </el-input>
      <el-select v-model="order" style="width: 140px" @change="onOrderChange">
        <el-option label="创建时间 ↓" value="desc" />
        <el-option label="创建时间 ↑" value="asc" />
      </el-select>
    </div>

    <FilesToolbar
      :can-go-up="currentPath !== '/'"
      :selected-count="selected.length"
      :dl-busy="dlBusy"
      :dl-done="dlDone"
      :dl-total="dlTotal"
      :dl-running="runningCount"
      @upload="uploadOpen = true"
      @new-dir="newDirOpen = true"
      @refresh="load"
      @go-up="goUp"
      @batch-delete="onBatchDelete"
      @batch-copy="onBatchCopy"
      @batch-move="onBatchMove"
      @download-selected="onDownloadSelected"
      @open-dl-tasks="dlOpen = true"
    />

    <div v-if="error" class="error-box">
      <el-result icon="error" :title="`加载失败:${error}`">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>
    </div>

    <FileListTable
      v-else
      :items="items"
      :loading="loading"
      :can-set-visibility="canSetVisibility"
      @select-change="selectionChanged"
      @enter-dir="enterDir"
      @download="onDownload"
      @download-folder="onDownloadFolder"
      @preview="openPreview"
      @share="openShare"
      @visibility="openVisibility"
      @rename="openRename"
      @move="(row) => openOp('move', row)"
      @copy="(row) => openOp('copy', row)"
      @delete="onDelete"
    />

    <div class="pager">
      <el-pagination
        :current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        @current-change="onPageChange"
      />
    </div>

    <!-- 弹窗 -->
    <UploadDialog v-model="uploadOpen" :bucket-id="bucketId" :dir-path="currentPath" @success="load" />
    <NewDirDialog v-model="newDirOpen" :bucket-id="bucketId" :dir-path="currentPath" @success="load" />
    <RenameDialog v-model="renameOpen" :bucket-id="bucketId" :dir-path="currentPath" :target="renameTarget" @success="load" />

    <MoveCopyDialog
      v-model="opOpen"
      :mode="opMode"
      :target="opTarget"
      :batch="batchPending"
      :bucket-id="bucketId"
      :current-path="currentPath"
      @success="load"
    />

    <ShareDialog v-model="shareOpen" :target="shareTarget" @success="load" />

    <VisibilityDialog
      v-model="visibilityOpen"
      :bucket-id="bucketId"
      :target="visibilityTarget"
      @success="load"
    />

    <PreviewDialog v-model="previewOpen" :bucket-id="bucketId" :file="previewTarget" />

    <DownloadTasksDialog
      v-model="dlOpen"
      :tasks="tasks"
      :progress="progress"
      :on-resume="resumeTask"
      :on-cancel="cancelTask"
      :on-clear="clearTask"
    />
  </AppLayout>
</template>

<style scoped>
.crumb-bar {
  margin-bottom: 12px;
}

.toolbar-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.error-box {
  display: flex;
  justify-content: center;
}

.pager {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}

.form-tip {
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
}
</style>
