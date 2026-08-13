<script setup lang="ts">
// 公开分享落地页:无需登录,访问 /share/:token。
// 流程:解析分享 → 需要提取码(401)则弹输入框 → 文件展示下载 / 文件夹浏览下载。
import { Back, Download, Folder, FolderOpened, Lock, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import {
  SharePasswordRequiredError,
  ShareUnavailableError,
  downloadSharedFile,
  resolveShare,
  saveBlob,
  type ShareFolderResult,
} from '@/api/shares'
import { formatSize, formatTime } from '@/api/types'

const route = useRoute()
const router = useRouter()
const token = computed(() => String(route.params.token || ''))

// 目录状态由 URL query ?path= 承载(分享根内相对路径;刷新/后退不丢,可分享具体目录):
//   与 /buckets/:id?path=xxx 同款交互语义 —— 面包屑任意级可点、返回上级、浏览器前进后退。
const path = computed(() => {
  const p = route.query.path
  const s = Array.isArray(p) ? p[0] : p
  return s ? `/${s.replace(/^\/+/, '').replace(/\/+$/, '')}` : '/'
})

const password = ref('') // 已输入的提取码(空 = 未尝试/不需要)
const loading = ref(false)
const resolving = ref(false) // 密码弹框输入中,避免重复弹
const error = ref('') // 分享失效/不存在的展示错误
const file = ref<ShareFolderResult | null>(null) // 文件分享(占位命名,实际存解析结果)
const isFolder = ref(false)
const metaName = ref('')
const page = ref(1)
const pageSize = ref(50)
const total = ref(0)
const dirs = ref<{ ID: number; Name: string }[]>([])
const files = ref<{ ID: number; Name: string; FileSize: number; UpdatedAt: string }[]>([])

/** 面包屑:分享根内路径分段(每级可点击跳转) */
const crumbs = computed(() => {
  const parts = path.value === '/' ? [] : path.value.split('/').filter(Boolean)
  return parts.map((name, i) => ({ name, index: i }))
})

function joinPath(name: string): string {
  return path.value === '/' ? name : `${path.value}/${name}`
}

/** 目录跳转:push query(浏览器后退可回);根目录移除 path 参数。
 *  提取码存于内存 ref(不写入 URL,避免密码落地址栏/历史),进入子目录后仍有效。 */
function goToPath(p: string) {
  const query: Record<string, string> = {}
  if (p && p !== '/') query.path = p.replace(/^\/+/, '')
  router.push({ name: 'share', params: { token: token.value }, query })
}

/** 进入子目录 */
function enterDir(name: string) {
  goToPath(joinPath(name))
}

/** 返回上级(根目录时禁用) */
function goUp() {
  if (path.value === '/') return
  const parts = path.value.split('/').filter(Boolean)
  parts.pop()
  goToPath(parts.length ? `/${parts.join('/')}` : '/')
}

/** 面包屑跳转到第 index 级(0 = 根) */
function crumbGo(index: number) {
  if (index <= 0) {
    goToPath('/')
    return
  }
  const parts = path.value.split('/').filter(Boolean)
  goToPath(`/${parts.slice(0, index).join('/')}`)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const rel = path.value === '/' ? '' : path.value.replace(/^\/+/, '')
    const res = await resolveShare(token.value, password.value, rel)
    if ('files' in res && 'folders' in res) {
      // 文件夹分享
      isFolder.value = true
      metaName.value = (res as ShareFolderResult).meta.Name
      dirs.value = res.folders.map((d) => ({ ID: d.ID, Name: d.Name }))
      files.value = res.files.map((f) => ({ ID: f.ID, Name: f.Name, FileSize: f.FileSize, UpdatedAt: f.UpdatedAt }))
      total.value = res.total
    } else {
      // 文件分享
      isFolder.value = false
      file.value = res as unknown as ShareFolderResult
    }
  } catch (e) {
    if (e instanceof SharePasswordRequiredError) {
      await askPassword()
      return
    }
    if (e instanceof ShareUnavailableError) {
      error.value = e.message
      return
    }
    error.value = e instanceof Error ? e.message : '分享解析失败'
  } finally {
    loading.value = false
  }
}

watch(
  () => route.query.path,
  () => {
    page.value = 1
    load()
  },
)

onMounted(() => {
  page.value = 1
  load()
})

/** 弹提取码输入框(取消则显示提示;输入后重载) */
async function askPassword() {
  if (resolving.value) return
  resolving.value = true
  try {
    const { value } = await ElMessageBox.prompt('该分享设置了提取码,请输入后访问', '需要提取码', {
      inputType: 'password',
      confirmButtonText: '确认',
      cancelButtonText: '取消',
      inputPattern: /\S+/,
      inputErrorMessage: '提取码不能为空',
    })
    password.value = value.trim()
    await load()
  } catch {
    error.value = '已取消访问'
  } finally {
    resolving.value = false
  }
}

/** 下载分享文件(文件分享 path 空;文件夹分享传分享根内相对路径) */
async function onDownload(relPath: string, name: string) {
  loading.value = true
  try {
    const blob = await downloadSharedFile(token.value, password.value, relPath)
    saveBlob(blob, name)
  } catch (e) {
    if (e instanceof SharePasswordRequiredError) {
      password.value = '' // 强制重新弹框
      await askPassword()
      return
    }
    ElMessage.error(e instanceof Error ? e.message : '下载失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="share-page">
    <el-card class="share-card">
      <template #header>
        <div class="share-header">
          <el-icon class="lock"><Lock /></el-icon>
          <span class="title">分享内容</span>
          <el-button link :icon="Refresh" class="refresh" @click="load">刷新</el-button>
        </div>
      </template>

      <!-- 失效/不存在 -->
      <el-result v-if="error" icon="error" :title="error" sub-title="如有疑问请联系分享者">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>

      <!-- 文件夹分享:目录浏览 -->
      <div v-else-if="isFolder" v-loading="loading">
        <div class="crumb-bar">
          <el-breadcrumb separator="/">
            <el-breadcrumb-item>
              <el-link type="primary" :underline="false" @click="crumbGo(0)">分享根目录</el-link>
            </el-breadcrumb-item>
            <el-breadcrumb-item v-for="(c, i) in crumbs" :key="c.index">
              <el-link
                :type="i === crumbs.length - 1 ? 'primary' : 'primary'"
                :underline="false"
                @click="crumbGo(c.index)"
              >
                {{ c.name }}
              </el-link>
            </el-breadcrumb-item>
          </el-breadcrumb>
          <el-button
            v-if="path !== '/'"
            link
            :icon="Back"
            class="crumb-up"
            @click="goUp"
          >
            返回上级
          </el-button>
        </div>

        <el-table :data="dirs" stripe size="small" empty-text="暂无子文件夹">
          <el-table-column label="名称" min-width="260">
            <template #default="{ row }">
              <el-icon class="icon folder"><FolderOpened /></el-icon>
              <el-link type="primary" @click="enterDir(row.Name)">{{ row.Name }}</el-link>
            </template>
          </el-table-column>
          <el-table-column label="类型" width="120"><template #default>文件夹</template></el-table-column>
          <el-table-column label="操作" width="120">
            <template #default="{ row }">
              <el-button link type="primary" :icon="Folder" @click="enterDir(row.Name)">打开</el-button>
            </template>
          </el-table-column>
        </el-table>

        <el-table :data="files" stripe size="small" empty-text="暂无文件" class="mt12">
          <el-table-column label="文件名" min-width="260">
            <template #default="{ row }">{{ row.Name }}</template>
          </el-table-column>
          <el-table-column label="大小" width="130">
            <template #default="{ row }">{{ formatSize(row.FileSize) }}</template>
          </el-table-column>
          <el-table-column label="更新时间" width="190">
            <template #default="{ row }">{{ formatTime(row.UpdatedAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="120">
            <template #default="{ row }">
              <el-button link type="primary" :icon="Download" @click="onDownload(joinPath(row.Name), row.Name)">
                下载
              </el-button>
            </template>
          </el-table-column>
        </el-table>

        <div v-if="total > pageSize" class="pager">
          <el-pagination
            :current-page="page"
            :page-size="pageSize"
            :total="total"
            layout="prev, pager, next"
            @current-change="(p: number) => { page = p; load() }"
          />
        </div>
      </div>

      <!-- 文件分享:单个文件下载 -->
      <div v-else v-loading="loading">
        <el-descriptions :column="1" border>
          <el-descriptions-item label="文件名">{{ metaName || (file as any)?.Name || '—' }}</el-descriptions-item>
          <el-descriptions-item label="大小">
            {{ formatSize((file as any)?.FileSize) }}
          </el-descriptions-item>
          <el-descriptions-item label="类型">{{ (file as any)?.FileType || '—' }}</el-descriptions-item>
          <el-descriptions-item label="更新时间">
            {{ formatTime((file as any)?.UpdatedAt) }}
          </el-descriptions-item>
        </el-descriptions>
        <div class="dl-btn">
          <el-button type="primary" size="large" :icon="Download" @click="onDownload('', (file as any)?.Name)">
            下载文件
          </el-button>
        </div>
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.share-page {
  min-height: 100%;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 48px 16px;
  background: #f5f7fa;
}

.share-card {
  width: 760px;
  max-width: 100%;
}

.share-header {
  display: flex;
  align-items: center;
  gap: 8px;
}

.share-header .title {
  font-weight: 600;
}

.share-header .lock {
  color: #e6a23c;
}

.share-header .refresh {
  margin-left: auto;
}

.crumb-bar {
  margin-bottom: 12px;
  display: flex;
  align-items: center;
  gap: 12px;
}

.crumb-bar .el-breadcrumb {
  flex: 1;
}

.crumb-up {
  flex-shrink: 0;
}

.icon {
  margin-right: 6px;
  vertical-align: -2px;
}

.icon.folder {
  color: #e6a23c;
}

.mt12 {
  margin-top: 12px;
}

.pager {
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
}

.dl-btn {
  margin-top: 20px;
  text-align: center;
}
</style>
