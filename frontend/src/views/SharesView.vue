<script setup lang="ts">
// 分享管理页:元素 = 工具栏(刷新) + 分享表格 + 分页
// 操作归属:复制链接(clipboard) / 删除(确认后);创建入口在文件页
import { CopyDocument, Delete, Link, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { onMounted, ref } from 'vue'

import { deleteShare, listShares, shareUrl } from '@/api/shares'
import type { ShareLink } from '@/api/types'
import { formatTime } from '@/api/types'
import AppLayout from '@/components/AppLayout.vue'
import { useListState } from '@/composables/useListState'

const { loading, error, run } = useListState()
const shares = ref<ShareLink[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)

async function load() {
  const res = await run(
    () => listShares({ page: page.value, page_size: pageSize.value }),
    (m) => ElMessage.error(m),
  )
  if (res) {
    shares.value = res.items
    total.value = res.total
  }
}

function onPageChange(p: number) {
  page.value = p
  load()
}

async function copyLink(row: ShareLink) {
  // 落地页链接(需要提取码时访问者会被引导输入;不再直链 API 端点)
  const url = shareUrl(row.Token)
  try {
    await navigator.clipboard.writeText(url)
    ElMessage.success('链接已复制')
  } catch {
    ElMessage.info(`复制失败,请手动复制:${url}`)
  }
}

async function onDelete(row: ShareLink) {
  try {
    await ElMessageBox.confirm(`删除分享「${row.Token}」?链接将立即失效。`, '删除分享', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await deleteShare(row.ID)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="toolbar">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <span class="hint">在文件列表中选中文件/文件夹 →「分享」即可创建分享链接</span>
    </div>

    <div v-if="error" class="error-box">
      <el-result icon="error" :title="`加载失败:${error}`">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>
    </div>

    <el-table v-else v-loading="loading" :data="shares" stripe style="width: 100%">
      <el-table-column label="分享码(Token)" width="180">
        <template #default="{ row }">
          <el-icon class="icon"><Link /></el-icon>
          <code class="token">{{ row.Token }}</code>
        </template>
      </el-table-column>
      <el-table-column label="条目 ID" prop="BucketItemID" width="100" />
      <el-table-column label="权限" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.Permission === 'edit' ? 'warning' : 'info'">
            {{ row.Permission }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="有效期" width="170">
        <template #default="{ row }">
          {{ row.ExpiresAt ? formatTime(row.ExpiresAt) : '永久' }}
        </template>
      </el-table-column>
      <el-table-column label="下载次数" width="130">
        <template #default="{ row }">
          {{ row.DownloadCount }} / {{ row.MaxDownloads > 0 ? row.MaxDownloads : '不限' }}
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ formatTime(row.CreatedAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="160" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" :icon="CopyDocument" @click="copyLink(row)">
            复制链接
          </el-button>
          <el-button link type="danger" size="small" :icon="Delete" @click="onDelete(row)">
            删除
          </el-button>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="暂无分享 — 在文件列表中选中条目点击「分享」" :image-size="80" />
      </template>
    </el-table>

    <div class="pager">
      <el-pagination
        v-model:current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        @current-change="onPageChange"
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
  margin-left: auto;
  font-size: 13px;
  color: #909399;
}

.error-box {
  display: flex;
  justify-content: center;
}

.icon {
  margin-right: 6px;
  vertical-align: -2px;
  color: #409eff;
}

.token {
  font-family: Consolas, monospace;
}

.pager {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}
</style>
