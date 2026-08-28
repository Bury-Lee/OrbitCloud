<script setup lang="ts">
// 预览对话框(自包含):图片 / 音视频(直链 Range 边下边播)/ PDF / 文本与代码。
// 可预览性判定统一走 utils/preview(扩展名回退:md/json/代码文件等均支持)。
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { previewFile, previewFileUrl } from '@/api/files'
import { isPreviewable, previewKind } from '@/utils/preview'

const props = defineProps<{
  modelValue: boolean
  bucketId: number
  file: { id: number; name: string; type?: string } | null
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
}>()

const loading = ref(false)
const url = ref('')
const kind = computed(() => (props.file ? previewKind(props.file.type || '') : 'text'))

function isBlobUrl(u: string): boolean {
  return u.startsWith('blob:')
}

// 打开时加载预览内容(音视频走直链,其余 blob)
watch(
  () => [props.modelValue, props.file?.id] as const,
  async ([open, fileId]) => {
    if (!open || !props.file || !fileId) return
    if (!isPreviewable(props.file.type || '', props.file.name)) {
      ElMessage.info('该类型暂不支持在线预览,请下载后查看')
      emit('update:modelValue', false)
      return
    }
    if (isBlobUrl(url.value)) URL.revokeObjectURL(url.value)
    url.value = ''
    loading.value = true
    try {
      const t = (props.file.type || '').toLowerCase()
      if (t.startsWith('video/') || t.startsWith('audio/')) {
        // 浏览器对 media 元素自动发 Range 请求(206),后端 /stream 支持 → 边下边播
        url.value = previewFileUrl(props.bucketId, fileId)
      } else {
        const blob = await previewFile(props.bucketId, fileId)
        url.value = URL.createObjectURL(blob)
      }
    } catch (e) {
      ElMessage.error(e instanceof Error ? e.message : '预览失败')
    } finally {
      loading.value = false
    }
  },
)

// 关闭时释放 blob
watch(
  () => props.modelValue,
  (open) => {
    if (!open && isBlobUrl(url.value)) {
      URL.revokeObjectURL(url.value)
      url.value = ''
    }
  },
)
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="file?.name"
    width="80%"
    top="5vh"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-loading="loading" class="preview-body">
      <div v-if="kind === 'image' && url" class="preview-box">
        <img :src="url" class="preview-img" alt="preview" />
      </div>
      <div v-else-if="kind === 'media' && url" class="preview-box">
        <video v-if="(file?.type || '').startsWith('video/')" :src="url" controls class="preview-media" />
        <audio v-else :src="url" controls class="preview-audio" />
      </div>
      <iframe v-else-if="url" :src="url" class="preview-frame" title="preview" />
      <el-empty v-else description="正在加载预览…" :image-size="80" />
    </div>
  </el-dialog>
</template>

<style scoped>
.preview-body {
  min-height: 300px;
}

.preview-box {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 300px;
  background: #f5f7fa;
}

.preview-img {
  max-width: 100%;
  max-height: 70vh;
  object-fit: contain;
}

.preview-media {
  max-width: 100%;
  max-height: 70vh;
}

.preview-audio {
  width: 80%;
}

.preview-frame {
  width: 100%;
  height: 70vh;
  border: none;
}
</style>
