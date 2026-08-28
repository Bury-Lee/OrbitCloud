<script setup lang="ts">
// 新建文件夹对话框(自包含):mkdir -p 语义,提交后父级刷新。
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { createDir } from '@/api/files'

const props = defineProps<{
  modelValue: boolean
  bucketId: number
  dirPath: string
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'success'): void
}>()

const name = ref('')
const creating = ref(false)

// 打开时清空输入框(防上次输入残留)
watch(
  () => props.modelValue,
  (open) => {
    if (open) name.value = ''
  },
)

function joinPath(dir: string, n: string): string {
  return dir === '/' ? `/${n}` : `${dir}/${n}`
}

async function onCreate() {
  const n = name.value.trim()
  if (!n) {
    ElMessage.warning('请输入文件夹名称')
    return
  }
  creating.value = true
  try {
    await createDir(props.bucketId, joinPath(props.dirPath, n))
    ElMessage.success('创建成功')
    emit('success')
    emit('update:modelValue', false)
    name.value = ''
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '创建失败')
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="新建文件夹"
    width="420px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-input v-model="name" placeholder="文件夹名称" maxlength="255" @keyup.enter="onCreate" />
    <div class="form-tip">创建于:{{ dirPath }}</div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="creating" @click="onCreate">创建</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.form-tip {
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
}
</style>
