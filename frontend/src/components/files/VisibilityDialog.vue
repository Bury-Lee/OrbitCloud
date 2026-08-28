<script setup lang="ts">
// 可见组设置对话框(自包含):目标条目 + 多选可见组(创建者或管理员)。
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { listGroups, setFileVisibility, setFolderVisibility } from '@/api/groups'
import { isFolder, type FileRow, type UserGroup } from '@/api/types'

const props = defineProps<{
  modelValue: boolean
  bucketId: number
  target: FileRow | null
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'success'): void
}>()

const groups = ref<number[]>([])
const groupOptions = ref<UserGroup[]>([])
const submitting = ref(false)

watch(
  () => props.modelValue,
  async (open) => {
    if (!open || !props.target) return
    groups.value = [...props.target.visibleGroups]
    try {
      // 管理员看全部组;普通用户后端仅返回自己所在组
      const res = await listGroups({ page: 1, page_size: 1000 })
      groupOptions.value = res.items
    } catch {
      groupOptions.value = []
    }
  },
)

async function onSave() {
  const target = props.target
  if (!target) return
  submitting.value = true
  try {
    if (isFolder(target)) {
      await setFolderVisibility(props.bucketId, target.id, groups.value)
    } else {
      await setFileVisibility(props.bucketId, target.id, groups.value)
    }
    ElMessage.success('可见组已更新')
    emit('success')
    emit('update:modelValue', false)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '设置失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="设置可见组"
    width="480px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form label-width="90px">
      <el-form-item label="条目">
        <span>{{ target?.name }}</span>
      </el-form-item>
      <el-form-item label="可见组">
        <el-select v-model="groups" multiple filterable clearable style="width: 100%">
          <el-option v-for="g in groupOptions" :key="g.ID" :value="g.ID" :label="g.Name" />
        </el-select>
        <div class="form-tip">
          选中组后仅组内成员可见(创建者与管理员始终可见);清空/取消全部 = 恢复按桶权限可见
        </div>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="onSave">保存</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.form-tip {
  font-size: 12px;
  color: #909399;
}
</style>
