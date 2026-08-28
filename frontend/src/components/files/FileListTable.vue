<script setup lang="ts">
// 文件表格(纯展示 + 事件上抛):名称/大小/类型/修改时间/操作列。
import { Files, Folder } from '@element-plus/icons-vue'

import { formatSize, formatTime, isFolder, type FileRow } from '@/api/types'

defineProps<{
  items: FileRow[]
  loading: boolean
  canSetVisibility: (row: FileRow) => boolean
}>()
const emit = defineEmits<{
  (e: 'select-change', rows: FileRow[]): void
  (e: 'enter-dir', row: FileRow): void
  (e: 'download', row: FileRow): void
  (e: 'download-folder', row: FileRow): void
  (e: 'preview', row: FileRow): void
  (e: 'share', row: FileRow): void
  (e: 'visibility', row: FileRow): void
  (e: 'rename', row: FileRow): void
  (e: 'move', row: FileRow): void
  (e: 'copy', row: FileRow): void
  (e: 'delete', row: FileRow): void
}>()
</script>

<template>
  <el-table
    v-loading="loading"
    :data="items"
    stripe
    style="width: 100%"
    @row-dblclick="emit('enter-dir', $event)"
    @selection-change="emit('select-change', $event)"
  >
    <el-table-column type="selection" width="45" />
    <el-table-column label="名称" min-width="260">
      <template #default="{ row }">
        <el-icon v-if="isFolder(row)" class="icon folder"><Folder /></el-icon>
        <el-icon v-else class="icon file"><Files /></el-icon>
        <el-link v-if="isFolder(row)" type="primary" :underline="false" @click="emit('enter-dir', row)">
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
          @click="emit('download-folder', row)"
        >
          下载
        </el-button>
        <el-button v-else link type="primary" size="small" @click="emit('download', row)">下载</el-button>
        <el-button v-if="!isFolder(row)" link type="primary" size="small" @click="emit('preview', row)">
          预览
        </el-button>
        <el-button link type="primary" size="small" @click="emit('share', row)">分享</el-button>
        <el-button
          v-if="canSetVisibility(row)"
          link
          type="primary"
          size="small"
          @click="emit('visibility', row)"
        >
          可见组
        </el-button>
        <el-button link type="primary" size="small" @click="emit('rename', row)">重命名</el-button>
        <el-button link type="primary" size="small" @click="emit('move', row)">移动</el-button>
        <el-button link type="primary" size="small" @click="emit('copy', row)">复制</el-button>
        <el-button link type="danger" size="small" @click="emit('delete', row)">删除</el-button>
      </template>
    </el-table-column>
    <template #empty>
      <el-empty description="目录为空 — 上传文件或新建文件夹" :image-size="80" />
    </template>
  </el-table>
</template>

<style scoped>
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
</style>
