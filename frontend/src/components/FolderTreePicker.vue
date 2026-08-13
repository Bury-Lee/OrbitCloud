<script setup lang="ts">
// FolderTreePicker.vue —— 复制/移动目标目录选择器(Windows 资源管理器式逐级导航)
// 顶部:路径预览(面包屑,可点击任意祖先级跳转);中部:当前目录的子文件夹列表,
// 点击文件夹即进入该目录(拼接路径预览 + 更新目标 folderId,并列出其子文件夹);
// 打开时自动定位到当前所在文件夹;换桶重置到桶根。提交仍走后端 dst_dir 路径
// 语义(后端逻辑不变),本组件只负责把 folderId 定位映射为路径。
import { ArrowUp, Folder, HomeFilled, Loading } from '@element-plus/icons-vue'
import { ref, watch } from 'vue'

import { listFilesCursor } from '@/api/files'

const props = defineProps<{ bucketId: number; currentPath?: string }>()
const emit = defineEmits<{ (e: 'pick', v: { folderId: number; path: string }): void }>()

/** 当前路径链(不含桶根;每级记录 folders.id,供面包屑任意级跳转) */
const trail = ref<{ id: number; name: string }[]>([])
const folders = ref<{ id: number; name: string }[]>([])
const loading = ref(false)
const locating = ref(false)

function currentPath(): string {
  return trail.value.length === 0 ? '/' : '/' + trail.value.map((s) => s.name).join('/')
}

function currentFolderId(): number {
  return trail.value.length === 0 ? 0 : trail.value[trail.value.length - 1].id
}

/** 游标翻页取尽某路径下的全部子文件夹(单级列表需完整,不受 offset 分页 total 限制) */
async function fetchFoldersAll(bucketId: number, path: string): Promise<{ ID: number; Name: string }[]> {
  const out: { ID: number; Name: string }[] = []
  let fc = ''
  let dc = ''
  do {
    const res = await listFilesCursor(bucketId, { path, files_cursor: fc, folders_cursor: dc, page_size: 100 })
    out.push(...res.folders)
    fc = res.next_files_cursor
    dc = res.next_folders_cursor
  } while (fc !== '' || dc !== '')
  return out
}

async function loadFolders() {
  loading.value = true
  try {
    const out = await fetchFoldersAll(props.bucketId, currentPath())
    folders.value = out.map((f) => ({ id: f.ID, name: f.Name }))
  } catch {
    folders.value = []
  } finally {
    loading.value = false
  }
}

function emitPick() {
  emit('pick', { folderId: currentFolderId(), path: currentPath() })
}

/** 进入子文件夹:拼接路径(预览) + 更新目标 folderId + 列出该级子文件夹 */
async function enterDir(id: number, name: string) {
  trail.value.push({ id, name })
  await loadFolders()
  emitPick()
}

/** 返回上级(Windows 资源管理器"向上"按钮) */
async function goUp() {
  if (trail.value.length === 0) return
  trail.value.pop()
  await loadFolders()
  emitPick()
}

/** 面包屑跳转:回到第 level 级祖先(level = trail 下标;越界回桶根) */
async function goTo(level: number) {
  if (level < 0) {
    trail.value = []
  } else {
    trail.value.splice(level + 1)
  }
  await loadFolders()
  emitPick()
}

/** 定位到当前所在路径:从桶根逐级解析并下钻(路径失效则停在最近可达级) */
async function locate(path: string) {
  locating.value = true
  try {
    trail.value = []
    const segs = (path ?? '/').split('/').filter(Boolean)
    let cur = '/'
    for (const seg of segs) {
      const kids = await fetchFoldersAll(props.bucketId, cur)
      const hit = kids.find((k) => k.Name === seg)
      if (!hit) break
      trail.value.push({ id: hit.ID, name: hit.Name })
      cur = '/' + trail.value.map((s) => s.name).join('/')
    }
    await loadFolders()
    emitPick()
  } finally {
    locating.value = false
  }
}

watch(
  () => props.bucketId,
  () => {
    trail.value = []
    void locate('/')
  },
)

watch(
  () => props.currentPath,
  (p) => {
    if (p !== undefined && p !== null) void locate(p)
  },
  { immediate: true },
)
</script>

<template>
  <div class="fp-wrap">
    <!-- 顶部:路径预览(面包屑,模仿 Windows 资源管理器地址栏) -->
    <div class="fp-crumbs">
      <el-tooltip content="回到桶根" placement="top">
        <span class="fp-root" :class="{ cur: trail.length === 0 }" @click="goTo(-1)">
          <el-icon><HomeFilled /></el-icon>
          <span>全部文件</span>
        </span>
      </el-tooltip>
      <template v-for="(seg, i) in trail" :key="seg.id">
        <span class="fp-sep">/</span>
        <span class="fp-crumb" :class="{ cur: i === trail.length - 1 }" @click="goTo(i)">
          {{ seg.name }}
        </span>
      </template>
      <el-tooltip content="返回上级" placement="top">
        <el-button class="fp-up" link size="small" :disabled="trail.length === 0" :icon="ArrowUp" @click="goUp" />
      </el-tooltip>
    </div>

    <!-- 中部:当前目录的子文件夹列表(点击进入 = 选定目标) -->
    <div v-loading="loading" class="fp-list">
      <div v-if="locating" class="fp-empty">
        <el-icon class="is-loading"><Loading /></el-icon> 正在定位当前文件夹…
      </div>
      <template v-else>
        <div v-for="d in folders" :key="d.id" class="fp-item" @click="enterDir(d.id, d.name)">
          <el-icon class="fp-folder"><Folder /></el-icon>
          <span class="fp-name">{{ d.name }}</span>
        </div>
        <div v-if="!loading && folders.length === 0" class="fp-empty">该目录下没有子文件夹</div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.fp-crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 6px 10px;
  background: #f5f7fa;
  border: 1px solid #e4e7ed;
  border-bottom: none;
  border-radius: 4px 4px 0 0;
  font-size: 13px;
  overflow-x: auto;
  white-space: nowrap;
}

.fp-root,
.fp-crumb {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 5px;
  border-radius: 3px;
  cursor: pointer;
  color: #606266;
  flex-shrink: 0;
}

.fp-root:hover,
.fp-crumb:hover {
  background: #e4e7ed;
  color: #409eff;
}

.fp-root.cur,
.fp-crumb.cur {
  color: #409eff;
  font-weight: 600;
}

.fp-sep {
  color: #c0c4cc;
  flex-shrink: 0;
}

.fp-up {
  margin-left: auto;
  flex-shrink: 0;
}

.fp-list {
  border: 1px solid #e4e7ed;
  border-top: none;
  border-radius: 0 0 4px 4px;
  max-height: 220px;
  overflow: auto;
  padding: 4px 0;
}

.fp-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  cursor: pointer;
  border-radius: 3px;
  font-size: 13px;
}

.fp-item:hover {
  background: #ecf5ff;
}

.fp-folder {
  color: #e6a23c;
  font-size: 14px;
  flex-shrink: 0;
}

.fp-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.fp-empty {
  padding: 20px 0;
  text-align: center;
  color: #909399;
  font-size: 13px;
}
</style>
