<script setup lang="ts">
// 系统运维页面(仅管理员):运行状况卡片 + 日志文件列表(下载/预览美化)。
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Download, Monitor, Refresh, View } from '@element-plus/icons-vue'

import { downloadLog, getSystemStatus, listLogs, previewLog, type LogFileInfo, type SystemStatus } from '@/api/system'
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

// ---- 日志预览(美化:行号 + 级别着色) ----
const PREVIEW_MAX_LINES = 5000 // 超长日志只预览前 N 行,完整内容走下载

interface LogLine {
  no: number
  /** 级别:INFO/WARN/ERROR/DEBUG(其余空串不着色) */
  level: string
  text: string
}

const previewOpen = ref(false)
const previewTitle = ref('')
const previewLines = ref<LogLine[]>([])
const previewLoading = ref(false)
const previewTruncated = ref(false)

/** 从日志行文本提取级别标记(如 "[INFO]"),供着色 */
function extractLevel(line: string): string {
  const m = /\[(DEBUG|INFO|WARN|ERROR|FATAL|WARNING)\]/i.exec(line)
  return m ? m[1].toUpperCase() : ''
}

const levelClass: Record<string, string> = {
  DEBUG: 'lv-debug',
  INFO: 'lv-info',
  WARN: 'lv-warn',
  ERROR: 'lv-error',
  FATAL: 'lv-error',
  WARNING: 'lv-warn',
}

async function onPreviewLog(row: LogFileInfo) {
  previewOpen.value = true
  previewTitle.value = row.name
  previewLoading.value = true
  previewLines.value = []
  previewTruncated.value = false
  try {
    const text = await previewLog(row.path)
    const lines = text.split(/\r?\n/)
    const shown = lines.slice(0, PREVIEW_MAX_LINES)
    previewTruncated.value = lines.length > PREVIEW_MAX_LINES
    previewLines.value = shown.map((t, i) => ({ no: i + 1, level: extractLevel(t), text: t }))
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '预览失败')
    previewOpen.value = false
  } finally {
    previewLoading.value = false
  }
}

const lineCount = computed(() => previewLines.value.length)

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
        <el-table-column label="操作" width="160" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :icon="View" :disabled="!row.complete" @click="onPreviewLog(row)">
              预览
            </el-button>
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

    <!-- 日志预览弹窗(行号 + 级别着色,等宽字体) -->
    <el-dialog v-model="previewOpen" :title="previewTitle" width="80%" top="4vh">
      <div v-loading="previewLoading" class="preview-wrap">
        <el-alert
          v-if="previewTruncated"
          type="warning"
          :closable="false"
          class="preview-alert"
          :title="`日志超过 ${PREVIEW_MAX_LINES} 行,仅预览前 ${PREVIEW_MAX_LINES} 行,完整内容请下载查看`"
        />
        <div v-if="lineCount > 0" class="preview-log">
          <div v-for="l in previewLines" :key="l.no" class="preview-line">
            <span class="preview-no">{{ l.no }}</span>
            <span class="preview-text">
              <span v-if="l.level" :class="['lv', levelClass[l.level]]">[{{ l.level }}]</span>
              {{ l.text }}
            </span>
          </div>
        </div>
        <el-empty v-else-if="!previewLoading" description="日志为空" :image-size="60" />
      </div>
      <template #footer>
        <el-button @click="previewOpen = false">关闭</el-button>
      </template>
    </el-dialog>
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

.preview-alert {
  margin-bottom: 10px;
}

.preview-wrap {
  min-height: 200px;
  max-height: 72vh;
  overflow: auto;
  background: #1e1e1e;
  border-radius: 6px;
}

.preview-log {
  font-family: 'JetBrains Mono', 'Cascadia Code', Consolas, Menlo, monospace;
  font-size: 12px;
  line-height: 1.6;
  padding: 8px 0;
}

.preview-line {
  display: flex;
  padding: 0 8px;
  white-space: pre-wrap;
  word-break: break-all;
}

.preview-line:hover {
  background: #2d2d2d;
}

.preview-no {
  flex-shrink: 0;
  width: 52px;
  text-align: right;
  padding-right: 12px;
  color: #6a737d;
  user-select: none;
}

.preview-text {
  color: #d4d4d4;
}

.lv {
  font-weight: 600;
  margin-right: 4px;
}

.lv-debug {
  color: #6a737d;
}
.lv-info {
  color: #4fc1ff;
}
.lv-warn {
  color: #ffb86c;
}
.lv-error {
  color: #ff6b6b;
}
</style>
