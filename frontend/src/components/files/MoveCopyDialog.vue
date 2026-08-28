<script setup lang="ts">
// 移动 / 复制对话框(单条/批量共用):目标桶 + 目录树选择器。
import { reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { listBuckets } from '@/api/buckets'
import { batchCopy, batchMove, copyFile, copyFolder, moveFile, moveFolder } from '@/api/files'
import { isFolder, type Bucket, type FileRow } from '@/api/types'
import FolderTreePicker from '@/components/FolderTreePicker.vue'

const props = defineProps<{
  modelValue: boolean
  mode: 'move' | 'copy'
  target: FileRow | null
  /** 批量模式待处理条目;非空 = 批量(提交后清空) */
  batch: FileRow[] | null
  bucketId: number
  currentPath: string
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'success'): void
}>()

const buckets = ref<Bucket[]>([])
const form = reactive({ dstBucketId: 0, dstDir: '/' })
const submitting = ref(false)
const pickerPath = ref('/')

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    form.dstBucketId = props.bucketId
    form.dstDir = '/'
    pickerPath.value = props.currentPath
    listBuckets()
      .then((bs) => {
        buckets.value = bs
      })
      .catch(() => {
        buckets.value = []
      })
  },
)

function onPick(picked: { folderId: number; path: string }) {
  form.dstDir = picked.path
}

async function onSubmit() {
  const dstDir = form.dstDir.trim() || '/'
  submitting.value = true
  try {
    // 批量模式:统一目标,逐条独立(源桶显式携带,后端无缺省回退)
    if (props.batch) {
      const items = props.batch.map((r) => ({
        kind: (isFolder(r) ? 'folder' : 'file') as 'folder' | 'file',
        id: r.id,
        src_bucket_id: props.bucketId,
      }))
      const res =
        props.mode === 'move'
          ? await batchMove(props.bucketId, items, {
              dst_bucket_id: form.dstBucketId,
              dst_dir: dstDir,
            })
          : await batchCopy(props.bucketId, items, {
              dst_bucket_id: form.dstBucketId,
              dst_dir: dstDir,
            })
      const ok = res.success.length
      const bad = res.failed.length
      if (bad === 0) {
        ElMessage.success(`操作成功:${ok} 项`)
      } else {
        const detail = res.failed.map((f) => `${f.name ?? ''}(${f.error})`).join('、')
        ElMessage.warning(`成功 ${ok} 项,失败 ${bad} 项:${detail}`)
      }
      emit('success')
      emit('update:modelValue', false)
      return
    }

    // 单条目模式
    const target = props.target
    if (!target) return
    const common = { dst_bucket_id: form.dstBucketId, dst_dir: dstDir }
    if (isFolder(target)) {
      if (props.mode === 'move') {
        await moveFolder(props.bucketId, target.id, common)
      } else {
        await copyFolder(props.bucketId, target.id, common)
      }
    } else if (props.mode === 'move') {
      await moveFile(props.bucketId, target.id, common)
    } else {
      await copyFile(props.bucketId, target.id, common)
    }
    ElMessage.success(props.mode === 'move' ? '移动成功' : '复制成功')
    emit('success')
    emit('update:modelValue', false)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="
      batch
        ? mode === 'move'
          ? `批量移动(${batch.length} 项)`
          : `批量复制(${batch.length} 项)`
        : mode === 'move'
          ? '移动'
          : '复制'
    "
    width="560px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="op-bucket">
      <el-select v-model="form.dstBucketId" style="width: 100%">
        <el-option v-for="b in buckets" :key="b.ID" :value="b.ID" :label="b.Name" />
      </el-select>
    </div>
    <FolderTreePicker :bucket-id="form.dstBucketId" :current-path="pickerPath" @pick="onPick" />
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="onSubmit">
        {{ mode === 'move' ? '移动' : '复制' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.op-bucket {
  margin-bottom: 10px;
}
</style>
