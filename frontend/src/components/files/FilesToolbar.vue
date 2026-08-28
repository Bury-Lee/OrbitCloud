<script setup lang="ts">
// 文件页工具栏:上传/新建/刷新/返回上级 + 批量操作(删除/复制/移动/下载)+ 下载任务入口。
import { Download, Folder, FolderAdd, Refresh, Upload } from '@element-plus/icons-vue'

defineProps<{
  canGoUp: boolean
  selectedCount: number
  dlBusy: boolean
  dlDone: number
  dlTotal: number
  dlRunning: number
}>()
const emit = defineEmits<{
  (e: 'upload'): void
  (e: 'new-dir'): void
  (e: 'refresh'): void
  (e: 'go-up'): void
  (e: 'batch-delete'): void
  (e: 'batch-copy'): void
  (e: 'batch-move'): void
  (e: 'download-selected'): void
  (e: 'open-dl-tasks'): void
}>()
</script>

<template>
  <div class="toolbar">
    <el-button type="primary" :icon="Upload" @click="emit('upload')">上传文件</el-button>
    <el-button :icon="FolderAdd" @click="emit('new-dir')">新建文件夹</el-button>
    <el-button :icon="Refresh" @click="emit('refresh')">刷新</el-button>
    <el-button v-if="canGoUp" :icon="Folder" @click="emit('go-up')">返回上级</el-button>
    <el-divider direction="vertical" />
    <el-button :disabled="selectedCount === 0" type="danger" plain @click="emit('batch-delete')">
      批量删除
    </el-button>
    <el-button :disabled="selectedCount === 0" @click="emit('batch-copy')">批量复制</el-button>
    <el-button :disabled="selectedCount === 0" @click="emit('batch-move')">批量移动</el-button>
    <el-button
      :disabled="selectedCount === 0 || dlBusy"
      :icon="Download"
      :loading="dlBusy"
      @click="emit('download-selected')"
    >
      {{ dlBusy ? `下载中 ${dlDone}/${dlTotal}` : '下载所选' }}
    </el-button>
    <el-button :icon="Download" @click="emit('open-dl-tasks')">
      下载任务
      <el-badge v-if="dlRunning > 0" :value="dlRunning" class="dl-badge" />
    </el-button>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}

.dl-badge {
  margin-left: 6px;
}
</style>
