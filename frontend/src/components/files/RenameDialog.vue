<script setup lang="ts">
// 重命名对话框(自包含):文件/文件夹共用,提交后父级刷新。
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { moveFile, moveFolder } from '@/api/files'
import { isFolder, type FileRow } from '@/api/types'

const props = defineProps<{
  modelValue: boolean
  bucketId: number
  dirPath: string
  target: FileRow | null
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'success'): void
}>()

const name = ref('')
const renaming = ref(false)

watch(
  () => [props.modelValue, props.target?.id] as const,
  ([open]) => {
    if (open && props.target) {
      name.value = props.target.name
    }
  },
)

async function onRename() {
  const target = props.target
  if (!target) return
  const n = name.value.trim()
  if (!n) {
    ElMessage.warning('请输入新名称')
    return
  }
  if (n === target.name) {
    emit('update:modelValue', false)
    return
  }
  renaming.value = true
  try {
    if (isFolder(target)) {
      await moveFolder(props.bucketId, target.id, { dst_dir: props.dirPath, filename: n })
    } else {
      await moveFile(props.bucketId, target.id, { dst_dir: props.dirPath, filename: n })
    }
    ElMessage.success('重命名成功')
    emit('success')
    emit('update:modelValue', false)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '重命名失败')
  } finally {
    renaming.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="重命名"
    width="420px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-input v-model="name" maxlength="255" @keyup.enter="onRename" />
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="renaming" @click="onRename">确定</el-button>
    </template>
  </el-dialog>
</template>
