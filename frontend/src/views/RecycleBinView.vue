<script setup lang="ts">
// 回收站页面:已删除(软删)且归属自己的文件/文件夹,支持恢复与永久删除。
// 文件/文件夹两个 Tab;管理员可传 user_id 查询他人回收站。
import { computed, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, FolderOpened, Refresh, RefreshLeft, Search } from '@element-plus/icons-vue'

import {
  deleteRecycleFile,
  listRecycleFiles,
  listRecycleFolders,
  restoreRecycleFile,
  type CommonPageResult,
} from '@/api/recycleBin'
import { formatSize, formatTime, type FileItem, type FolderItem, type User } from '@/api/types'
import { listUsers } from '@/api/users'
import AppLayout from '@/components/AppLayout.vue'
import { useListState } from '@/composables/useListState'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const { loading, error, run } = useListState()

const tab = ref<'files' | 'folders'>('files')
const page = ref(1)
const pageSize = ref(50)
const userId = ref<number | undefined>(undefined)
const keyword = ref('')
const order = ref('desc') // 排序方向(字段固定为删除时间;仅 asc/desc)

const files = ref<FileItem[]>([])
const folders = ref<FolderItem[]>([])
const total = ref(0)
const userOptions = ref<User[]>([])

// 管理员查询他人回收站:加载用户下拉
if (auth.isAdmin) {
  listUsers({ page: 1, page_size: 1000 })
    .then((res) => {
      userOptions.value = res.items
    })
    .catch(() => {
      userOptions.value = []
    })
}

// 合并文件/文件夹为统一行结构(文件夹行无 size)
interface RecycleRow {
  id: number
  name: string
  size?: number
  deletedAt: string
  raw: FileItem | FolderItem
}

const rows = computed<RecycleRow[]>(() =>
  tab.value === 'files'
    ? files.value.map((f) => ({ id: f.ID, name: f.Name, size: f.FileSize, deletedAt: f.DeletedAt ?? '', raw: f }))
    : folders.value.map((f) => ({ id: f.ID, name: f.Name, deletedAt: f.DeletedAt ?? '', raw: f })),
)

function params() {
  return {
    page: page.value,
    page_size: pageSize.value,
    key: keyword.value || undefined,
    order: order.value,
    ...(userId.value ? { user_id: userId.value } : {}),
  }
}

async function load() {
  const p = params()
  const res = await run(async () => {
    const r = tab.value === 'files' ? await listRecycleFiles(p) : await listRecycleFolders(p)
    return { list: r, p }
  })
  if (!res) return
  if (tab.value === 'files') {
    files.value = (res.list as CommonPageResult<FileItem>).list
  } else {
    folders.value = (res.list as CommonPageResult<FolderItem>).list
  }
  total.value = res.list.total
}

watch([tab, page, userId, keyword, order], load, { immediate: true })

// 搜索防抖(输入停止 300ms 后触发)
let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearchInput() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    page.value = 1
    load()
  }, 300)
}

function onTabChange() {
  page.value = 1
}

async function onRestore(row: RecycleRow) {
  try {
    const f = await restoreRecycleFile(row.id)
    const hint = f.Name !== row.name ? `(已重命名为 ${f.Name})` : f.FolderID === 0 ? '(原目录已删,已移至根目录)' : ''
    ElMessage.success(`已恢复${hint || ''}`)
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '恢复失败')
  }
}

async function onDelete(row: RecycleRow) {
  try {
    await ElMessageBox.confirm(
      `永久删除「${row.name}」?该操作不可恢复,将立即释放空间。`,
      '永久删除确认',
      { type: 'warning', confirmButtonText: '永久删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    await deleteRecycleFile(row.id)
    ElMessage.success('已永久删除')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

function fmtSize(row: RecycleRow): string {
  return row.size === undefined ? '—' : formatSize(row.size)
}
</script>

<template>
  <AppLayout>
    <div class="toolbar">
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
      <el-select v-model="order" style="width: 140px" @change="page = 1">
        <el-option label="删除时间 ↓" value="desc" />
        <el-option label="删除时间 ↑" value="asc" />
      </el-select>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-select
        v-if="auth.isAdmin"
        v-model="userId"
        placeholder="查询用户(留空 = 自己)"
        clearable
        filterable
        style="width: 220px"
        @change="page = 1"
      >
        <el-option v-for="u in userOptions" :key="u.ID" :value="u.ID" :label="u.Username" />
      </el-select>
      <span class="hint">回收站保留期内可恢复;超过保留期(默认 30 天)由后台自动清除</span>
    </div>

    <el-tabs v-model="tab" @tab-change="onTabChange">
      <el-tab-pane label="文件" name="files" />
      <el-tab-pane label="文件夹" name="folders" />
    </el-tabs>

    <div v-if="error" class="error-box">
      <el-result icon="error" :title="`加载失败:${error}`">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>
    </div>

    <el-table v-else v-loading="loading" :data="rows" stripe style="width: 100%">
      <el-table-column label="名称" min-width="280">
        <template #default="{ row }">
          <el-icon class="icon" :class="tab === 'folders' ? 'folder' : 'file'">
            <FolderOpened v-if="tab === 'folders'" />
            <Delete v-else />
          </el-icon>
          {{ row.name }}
        </template>
      </el-table-column>
      <el-table-column label="大小" width="120">
        <template #default="{ row }">{{ fmtSize(row) }}</template>
      </el-table-column>
      <el-table-column label="删除时间" width="180">
        <template #default="{ row }">{{ formatTime(row.deletedAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="{ row }">
          <el-button v-if="tab === 'files'" link type="primary" :icon="RefreshLeft" @click="onRestore(row)">
            恢复
          </el-button>
          <el-button link type="danger" :icon="Delete" @click="onDelete(row)">永久删除</el-button>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="回收站为空" :image-size="80" />
      </template>
    </el-table>

    <div class="pager">
      <el-pagination
        :current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        @current-change="(p: number) => (page = p)"
      />
    </div>
  </AppLayout>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}

.hint {
  font-size: 12px;
  color: #909399;
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
</style>
