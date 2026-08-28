<script setup lang="ts">
// 系统运维页面(仅管理员):运行状况卡片 + 日志文件列表/下载。
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Download, Monitor, Refresh } from '@element-plus/icons-vue'

import { downloadLog, getSystemStatus, listLogs, type LogFileInfo, type SystemStatus } from '@/api/system'
import { formatSize, formatTime } from '@/api/types'
import AppLayout from '@/components/AppLayout.vue'

const status = ref<SystemStatus | null>(null)
const logs = ref<LogFileInfo[]>([])
const loading = ref(false)

function fmtUptime(seconds: number): string {
  if (!seconds || seconds <= 0) return '-'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  return d > 0 ? `${d}天 ${h}时 ${m}分` : h > 0 ? `${h}时 ${m}分` : `${m}分`
}

async function load() {
  loading.value = true
  try {
    const [s, l] = await Promise.all([getSystemStatus(), listLogs()])
    status.value = s
    logs.value = l.files ?? []
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '加载失败')
  } finally {
    loading.value = false
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

async function onDownloadLog(row: LogFileInfo) {
  try {
    const blob = await downloadLog(row.path)
    saveBlob(blob, row.name)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '下载失败')
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="toolbar">
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      <span class="hint">仅管理员可见;日志保留期由 log.retention_days 控制</span>
    </div>

    <div v-loading="loading" class="cards">
      <el-card class="card">
        <template #header>运行状况</template>
        <div class="kv"><span>版本 / 环境</span><b>{{ status?.version }} / {{ status?.environment }}</b></div>
        <div class="kv"><span>启动时间</span><b>{{ status?.boot_time ? formatTime(status.boot_time) : '-' }}</b></div>
        <div class="kv"><span>运行时长</span><b>{{ fmtUptime(status?.uptime_seconds ?? 0) }}</b></div>
        <div class="kv"><span>Go 版本</span><b>{{ status?.go_version }}</b></div>
        <div class="kv"><span>CPU 核数</span><b>{{ status?.num_cpu }}</b></div>
      </el-card>

      <el-card class="card">
        <template #header>资源与连通性</template>
        <div class="kv"><span>协程数</span><b>{{ status?.num_goroutine }}</b></div>
        <div class="kv"><span>堆内存</span><b>{{ formatSize(status?.heap_alloc_bytes ?? 0) }}</b></div>
        <div class="kv">
          <span>数据库</span>
          <el-tag :type="status?.db_alive ? 'success' : 'danger'" size="small">
            {{ status?.db_alive ? '正常' : '异常' }}
          </el-tag>
        </div>
        <div class="kv">
          <span>对象存储</span>
          <el-tag :type="status?.storage_alive ? 'success' : 'danger'" size="small">
            {{ status?.storage_alive ? '正常' : '异常' }}
          </el-tag>
        </div>
      </el-card>
    </div>

    <el-card class="log-card">
      <template #header>
        <div class="log-head">
          <span><el-icon><Monitor /></el-icon> 日志文件</span>
          <span class="hint">共 {{ logs.length }} 个</span>
        </div>
      </template>
      <el-table :data="logs" stripe size="small" style="width: 100%">
        <el-table-column label="日期目录" width="160" prop="dir" />
        <el-table-column label="文件名" min-width="260" prop="name" />
        <el-table-column label="大小" width="120">
          <template #default="{ row }">{{ formatSize(row.size) }}</template>
        </el-table-column>
        <el-table-column label="修改时间" width="180">
          <template #default="{ row }">{{ formatTime(row.mod_time) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :icon="Download" :disabled="!row.complete" @click="onDownloadLog(row)">
              下载
            </el-button>
          </template>
        </el-table-column>
        <template #empty>
          <el-empty description="无日志文件(未启用文件日志或目录为空)" :image-size="80" />
        </template>
      </el-table>
    </el-card>
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

.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 14px;
  margin-bottom: 14px;
}

.kv {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 0;
  font-size: 13px;
}

.kv span {
  color: #909399;
}

.log-card {
  margin-bottom: 14px;
}

.log-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
</style>
