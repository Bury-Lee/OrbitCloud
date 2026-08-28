<script setup lang="ts">
// 上传对话框(自包含):文件/文件夹混合选择 + 拖拽,自动复刻目录结构,进度展示。
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { UploadFilled } from '@element-plus/icons-vue'

import { formatSize } from '@/api/types'
import {
  collectFromDataTransfer,
  collectFromFiles,
  uploadStructured,
  type UploadTreeItem,
} from '@/composables/useStructuredUpload'

const props = defineProps<{
  modelValue: boolean
  bucketId: number
  dirPath: string
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'success'): void
}>()

const uploading = ref(false)
const uploadPercent = ref(0)
const uploadLoaded = ref(0)
const uploadTotal = ref(0)
const uploadItems = ref<UploadTreeItem[]>([])
const pickFilesInput = ref<HTMLInputElement>()
const pickFolderInput = ref<HTMLInputElement>()

function onPickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  const files = input.files
  if (files?.length) {
    uploadItems.value = collectFromFiles(files)
  }
  input.value = ''
}

function onPickFolder(e: Event) {
  const input = e.target as HTMLInputElement
  const files = input.files
  if (files?.length) {
    uploadItems.value = collectFromFiles(files)
  }
  input.value = ''
}

/** 拖拽 drop 时解析目录树;文件/文件夹混合;preventDefault 阻止浏览器默认打开文件 */
function onDrop(e: DragEvent) {
  if (!e.dataTransfer) return
  e.preventDefault()
  void collectFromDataTransfer(e.dataTransfer).then((items) => {
    if (items.length === 0) {
      ElMessage.warning('未解析到文件,请尝试用「选择文件/文件夹」按钮')
      return
    }
    uploadItems.value = [...uploadItems.value, ...items]
  })
}

function onDragOver(e: DragEvent) {
  e.preventDefault()
}

function removeUploadItem(idx: number) {
  uploadItems.value.splice(idx, 1)
}

async function onUpload() {
  if (uploadItems.value.length === 0) {
    ElMessage.warning('请先选择要上传的文件或文件夹')
    return
  }
  uploading.value = true
  uploadPercent.value = 0
  uploadLoaded.value = 0
  uploadTotal.value = 0
  try {
    const res = await uploadStructured(props.bucketId, uploadItems.value, props.dirPath, (done, total) => {
      uploadLoaded.value = done
      uploadTotal.value = total
      uploadPercent.value = total > 0 ? Math.round((done / total) * 100) : 0
    })
    const ok = res.ok
    const bad = res.failed.length
    if (bad === 0) {
      ElMessage.success(`上传完成:${ok} 项`)
    } else {
      const detail = res.failed.map((f) => `${f.relPath}(${f.error})`).join('、')
      ElMessage.warning(`上传成功 ${ok} 项,失败 ${bad} 项:${detail}`)
    }
    emit('success')
    emit('update:modelValue', false)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '上传失败')
  } finally {
    uploading.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="上传文件 / 文件夹"
    width="560px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="upload-dropzone" @dragover="onDragOver" @drop="onDrop">
      <el-icon class="upload-icon"><UploadFilled /></el-icon>
      <div class="el-upload__text">将文件/文件夹拖到此处(自动复刻目录结构)</div>
      <div class="upload-actions">
        <el-button size="small" @click="pickFilesInput?.click()">选择文件</el-button>
        <el-button size="small" @click="pickFolderInput?.click()">选择文件夹</el-button>
      </div>
      <input ref="pickFilesInput" type="file" multiple class="upload-hidden-input" @change="onPickFiles" />
      <input ref="pickFolderInput" type="file" webkitdirectory class="upload-hidden-input" @change="onPickFolder" />
    </div>

    <div v-if="uploadItems.length > 0" class="upload-items">
      <div class="upload-items-head">
        <span>已选 {{ uploadItems.length }} 项</span>
      </div>
      <div class="upload-items-list">
        <div v-for="(it, idx) in uploadItems" :key="`${it.relPath}-${idx}`" class="upload-item">
          <span class="upload-item-name" :title="it.relPath">{{ it.relPath }}</span>
          <span class="upload-item-size">{{ formatSize(it.file.size) }}</span>
          <el-button link type="danger" size="small" @click="removeUploadItem(idx)">移除</el-button>
        </div>
      </div>
    </div>

    <div class="upload-target">上传到目录:{{ dirPath }}</div>
    <el-progress v-if="uploading" :percentage="uploadPercent" :stroke-width="10" class="upload-progress" />
    <div v-if="uploading" class="upload-progress-text">
      已上传 {{ formatSize(uploadLoaded) }} / {{ formatSize(uploadTotal) }}
      <span v-if="uploadTotal > 0">({{ uploadPercent }}%)</span>
    </div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="uploading" @click="onUpload">
        {{ uploading ? `上传中 ${uploadPercent}%` : '开始上传' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.upload-icon {
  font-size: 56px;
  color: #c0c4cc;
}

.upload-dropzone {
  border: 1px dashed #dcdfe6;
  border-radius: 6px;
  padding: 24px 12px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  background: #fafafa;
  transition: border-color 0.2s, background 0.2s;
}

.upload-dropzone:hover,
.upload-dropzone.dragover {
  border-color: #409eff;
  background: #f0f7ff;
}

.upload-hidden-input {
  display: none;
}

.upload-actions {
  display: flex;
  gap: 8px;
}

.upload-items {
  margin-top: 10px;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  max-height: 180px;
  overflow-y: auto;
}

.upload-items-head {
  padding: 6px 10px;
  font-size: 12px;
  color: #909399;
  border-bottom: 1px solid #ebeef5;
}

.upload-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  font-size: 13px;
}

.upload-item-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.upload-item-size {
  color: #909399;
  font-size: 12px;
  flex-shrink: 0;
}

.upload-target,
.form-tip {
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
}

.upload-progress-text {
  margin-top: 4px;
  font-size: 12px;
  color: #606266;
  text-align: center;
}

.upload-progress {
  margin-top: 12px;
}
</style>
