<script setup lang="ts">
// 下载任务面板(文件夹递归下载:进度/断点在前端 IndexedDB)。
// 状态与方法由 useFolderDownload 提供,经 props 传入(纯展示组件)。
import type { DownloadTaskState } from '@/composables/useFolderDownload'

defineProps<{
  modelValue: boolean
  tasks: DownloadTaskState[]
  progress: (t: DownloadTaskState) => { done: number; failed: number; total: number }
  onResume: (id: string) => void
  onCancel: (id: string) => void
  onClear: (id: string) => void
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
}>()

function percent(p: { done: number; total: number }): number {
  return p.total > 0 ? Math.round((p.done / p.total) * 100) : 0
}

const statusLabel: Record<string, string> = {
  running: '下载中',
  done: '已完成',
  failed: '失败',
  cancelled: '已取消',
}

const statusTag: Record<string, 'primary' | 'success' | 'warning' | 'info' | 'danger'> = {
  running: 'primary',
  done: 'success',
  failed: 'danger',
  cancelled: 'info',
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="下载任务"
    width="560px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-if="tasks.length === 0" class="dl-empty">
      暂无下载任务 — 点击文件夹行的「下载」开始递归下载
    </div>
    <div v-for="t in tasks" :key="t.id" class="dl-task">
      <div class="dl-task-head">
        <span class="dl-task-name">{{ t.rootName }}</span>
        <el-tag size="small" :type="statusTag[t.status] ?? 'info'">
          {{ statusLabel[t.status] ?? t.status }}
        </el-tag>
      </div>
      <el-progress
        :percentage="percent(progress(t))"
        :status="t.status === 'failed' ? 'exception' : t.status === 'done' ? 'success' : undefined"
        :stroke-width="10"
      />
      <div class="dl-task-meta">
        已完成 {{ progress(t).done }}/{{ progress(t).total }}
        <span v-if="progress(t).failed > 0">· 失败 {{ progress(t).failed }}</span>
        <span>· 递归深度 {{ t.maxDepth }}</span>
      </div>
      <div class="dl-task-ops">
        <el-button
          v-if="t.status === 'failed' || t.status === 'cancelled'"
          link
          type="primary"
          size="small"
          @click="onResume(t.id)"
        >
          继续
        </el-button>
        <el-button v-if="t.status === 'running'" link type="danger" size="small" @click="onCancel(t.id)">
          取消
        </el-button>
        <el-button link type="info" size="small" @click="onClear(t.id)">清除记录</el-button>
      </div>
    </div>
  </el-dialog>
</template>

<style scoped>
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
</style>
