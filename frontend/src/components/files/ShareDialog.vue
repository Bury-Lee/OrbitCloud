<script setup lang="ts">
// 分享创建对话框(自包含):表单(下载上限/有效期/提取码)→ 结果(链接复制)。
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { CopyDocument } from '@element-plus/icons-vue'

import { createShare, shareUrl } from '@/api/shares'
import { formatTime, type FileRow, type ShareLink } from '@/api/types'

const props = defineProps<{
  modelValue: boolean
  target: FileRow | null
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'success'): void
}>()

const submitting = ref(false)
const result = ref<ShareLink | null>(null)
const form = reactive({
  maxDownloads: 0,
  expiresDays: 0,
  password: '',
})

async function open() {
  result.value = null
  form.maxDownloads = 0
  form.expiresDays = 0
  form.password = ''
}

async function onCreateShare() {
  const target = props.target
  if (!target) return
  submitting.value = true
  try {
    const expiresAt =
      form.expiresDays > 0
        ? new Date(Date.now() + form.expiresDays * 24 * 3600 * 1000).toISOString()
        : null
    result.value = await createShare({
      file_id: target.id,
      max_downloads: form.maxDownloads,
      expires_at: expiresAt,
      password: form.password || undefined,
    })
    ElMessage.success('分享创建成功')
    emit('success')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '创建分享失败')
  } finally {
    submitting.value = false
  }
}

async function copyShareLink() {
  if (!result.value) return
  const url = `${shareUrl(result.value.Token)}?download=1`
  try {
    await navigator.clipboard.writeText(url)
    ElMessage.success('链接已复制(带提取码访问时浏览器会提示输入)')
  } catch {
    ElMessage.info(`复制失败,请手动复制:${url}`)
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="创建分享"
    width="480px"
    @update:model-value="emit('update:modelValue', $event)"
    @open="open"
  >
    <template v-if="!result">
      <el-form label-width="90px">
        <el-form-item label="分享对象">
          <span>{{ target?.name }}</span>
        </el-form-item>
        <el-form-item label="下载上限">
          <el-input-number v-model="form.maxDownloads" :min="0" style="width: 100%" />
          <div class="form-tip">0 = 不限次数</div>
        </el-form-item>
        <el-form-item label="有效期">
          <el-input-number v-model="form.expiresDays" :min="0" :max="365" style="width: 100%" />
          <div class="form-tip">天数;0 = 永久有效</div>
        </el-form-item>
        <el-form-item label="提取码">
          <el-input v-model="form.password" placeholder="可选,留空则无需提取码" maxlength="32" />
        </el-form-item>
      </el-form>
    </template>
    <template v-else>
      <el-result icon="success" title="分享创建成功">
        <template #sub-title>
          <div class="share-result">
            <div>访问链接(直接下载):</div>
            <el-input :model-value="`${shareUrl(result.Token)}?download=1`" readonly />
            <div v-if="form.password" class="form-tip">提取码:{{ form.password }}</div>
            <div class="form-tip">
              有效期:{{ result.ExpiresAt ? formatTime(result.ExpiresAt) : '永久' }} ·
              下载上限:{{ result.MaxDownloads > 0 ? `${result.MaxDownloads} 次` : '不限' }}
            </div>
          </div>
        </template>
        <template #extra>
          <el-button type="primary" :icon="CopyDocument" @click="copyShareLink">复制链接</el-button>
          <el-button @click="emit('update:modelValue', false)">完成</el-button>
        </template>
      </el-result>
    </template>
    <template v-if="!result" #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="onCreateShare">创建</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.share-result {
  text-align: left;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.form-tip {
  font-size: 12px;
  color: #909399;
}
</style>
