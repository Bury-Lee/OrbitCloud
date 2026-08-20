<script setup lang="ts">
// 我的网盘(桶列表):元素 = 工具栏(新建桶/刷新) + 桶表格 + 弹窗(新建/编辑)
// 操作归属:进入 → /buckets/:id;新建/编辑 → 弹窗;删除 → 确认后刷新
import { FolderOpened, Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'

import { createBucket, deleteBucket, listBuckets, updateBucket } from '@/api/buckets'
import type { Bucket } from '@/api/types'
import { formatSize, formatTime, permissionLabel } from '@/api/types'
import AppLayout from '@/components/AppLayout.vue'
import { useDialog } from '@/composables/useDialog'
import { useListState } from '@/composables/useListState'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()

const dialog = useDialog(['create-bucket', 'edit-bucket'])
const createDialog = dialog.model('create-bucket')
const editDialog = dialog.model('edit-bucket')
const { loading, error, run } = useListState()

const buckets = ref<Bucket[]>([])

async function load() {
  buckets.value = (await run(listBuckets, (m) => ElMessage.error(m))) ?? buckets.value
}

// ---- 新建桶 ----
const createFormRef = ref<FormInstance>()
const creating = ref(false)
const createForm = reactive({ name: '', description: '' })

const createRules: FormRules = {
  name: [
    { required: true, message: '请输入桶名', trigger: 'blur' },
    {
      pattern: /^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/,
      message: '桶名须为 3-63 位小写字母/数字/点/连字符(符合 S3 命名规范)',
      trigger: 'blur',
    },
  ],
}

async function onCreate() {
  if (!createFormRef.value) return
  const valid = await createFormRef.value.validate().catch(() => false)
  if (!valid) return
  creating.value = true
  try {
    await createBucket({ name: createForm.name, description: createForm.description })
    ElMessage.success('创建成功')
    dialog.close()
    createForm.name = ''
    createForm.description = ''
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '创建失败')
  } finally {
    creating.value = false
  }
}

// ---- 编辑桶 ----
const editing = ref(false)
const editForm = reactive({ id: 0, description: '', quota: 0, status: 1, permissionLevel: 3, managePermissionLevel: 0 })

/** 可选访问等级(0~3;低于该等级的用户不可访问本桶) */
const levelOptions = [0, 1, 2, 3]
/** 可选管理等级(0 = 跟随访问等级,单独列项;管理要求不得松于访问要求) */
const manageOptions = [1, 2, 3]

function openEdit(row: Bucket) {
  editForm.id = row.ID
  editForm.description = row.Description
  editForm.quota = row.Quota
  editForm.status = row.Status
  editForm.permissionLevel = row.PermissionLevel
  editForm.managePermissionLevel = row.ManagePermissionLevel ?? 0
  dialog.open('edit-bucket')
}

/** 访问等级设为 0(仅超管可访问)属高危操作,连续三次确认告知后果 */
async function confirmLockoutLevel(): Promise<boolean> {
  const warnings = [
    '警告(1/3):把访问等级设为「超级管理员」后,只有超级管理员能访问此桶,连桶拥有者和管理员都会被锁在外面。',
    '警告(2/3):此操作之后,非超级管理员将无法修改回原等级,恢复需要超级管理员介入。',
    '最终确认(3/3):确定要把本桶访问等级设为 0(仅超级管理员可访问)吗?',
  ]
  for (const msg of warnings) {
    try {
      await ElMessageBox.confirm(msg, '高危操作确认', {
        type: 'error',
        confirmButtonText: '继续',
        cancelButtonText: '取消',
      })
    } catch {
      return false
    }
  }
  return true
}

async function onEdit() {
  if (editForm.permissionLevel === 0) {
    const ok = await confirmLockoutLevel()
    if (!ok) return
  }
  editing.value = true
  try {
    await updateBucket(editForm.id, {
      description: editForm.description,
      quota: editForm.quota,
      status: editForm.status,
      permission_level: editForm.permissionLevel,
      manage_permission_level: editForm.managePermissionLevel,
    })
    ElMessage.success('已保存')
    dialog.close()
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    editing.value = false
  }
}

// ---- 删除桶 ----
async function onDelete(row: Bucket) {
  try {
    await ElMessageBox.confirm(
      `删除桶「${row.Name}」将级联删除桶内全部文件且不可恢复,确认继续?`,
      '删除桶',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    await deleteBucket(row.ID)
    ElMessage.success('删除任务已提交,后台将清理桶内文件')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

function openFiles(row: Bucket) {
  router.push(`/buckets/${row.ID}`)
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="toolbar">
      <el-button type="primary" :icon="Plus" @click="dialog.open('create-bucket')">新建桶</el-button>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <span class="hint">权限等级:{{ permissionLabel(auth.user?.PermissionLevel ?? -1) }}</span>
    </div>

    <div v-if="error" class="error-box">
      <el-result icon="error" :title="`加载失败:${error}`">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>
    </div>

    <el-table v-else v-loading="loading" :data="buckets" stripe style="width: 100%">
      <el-table-column label="桶名" min-width="180">
        <template #default="{ row }">
          <el-link type="primary" :underline="false" @click="openFiles(row)">
            <el-icon class="bucket-icon"><FolderOpened /></el-icon>
            {{ row.Name }}
          </el-link>
        </template>
      </el-table-column>
      <el-table-column prop="Description" label="描述" min-width="160" show-overflow-tooltip />
      <el-table-column label="权限等级" width="120">
        <template #default="{ row }">
          <el-tag size="small" effect="plain">{{ permissionLabel(row.PermissionLevel) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="管理等级" width="140">
        <template #default="{ row }">
          <el-tag size="small" effect="plain" type="warning">
            {{ (row.ManagePermissionLevel ?? 0) === 0 ? '跟随访问等级' : permissionLabel(row.ManagePermissionLevel) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="容量" width="150">
        <template #default="{ row }">
          {{ formatSize(row.UsedSpace) }} / {{ row.Quota > 0 ? formatSize(row.Quota) : '不限' }}
        </template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.Status === 1 ? 'success' : 'danger'">
            {{ row.Status === 1 ? '正常' : '禁用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="更新时间" width="170">
        <template #default="{ row }">{{ formatTime(row.UpdatedAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="200" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" @click="openFiles(row)">进入</el-button>
          <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" size="small" @click="onDelete(row)">删除</el-button>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="暂无可见桶,点击右上角「新建桶」开始使用" :image-size="80" />
      </template>
    </el-table>

    <!-- 新建桶 -->
    <el-dialog v-model="createDialog" title="新建桶" width="460px">
      <el-form ref="createFormRef" :model="createForm" :rules="createRules" label-width="80px">
        <el-form-item label="桶名" prop="name">
          <el-input v-model="createForm.name" placeholder="如 my-bucket-01" maxlength="63" />
        </el-form-item>
        <el-form-item label="描述" prop="description">
          <el-input v-model="createForm.description" placeholder="可选,说明桶用途" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="creating" @click="onCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- 编辑桶 -->
    <el-dialog v-model="editDialog" title="编辑桶" width="460px">
      <el-form label-width="80px">
        <el-form-item label="描述">
          <el-input v-model="editForm.description" maxlength="255" />
        </el-form-item>
        <el-form-item label="访问等级">
          <el-select v-model="editForm.permissionLevel" style="width: 100%">
            <el-option v-for="lv in levelOptions" :key="lv" :value="lv" :label="permissionLabel(lv)" />
          </el-select>
          <div class="form-tip">低于该等级的用户不可访问本桶;设为「超级管理员」将锁死所有非超管用户(高危)</div>
        </el-form-item>
        <el-form-item label="管理等级">
          <el-select v-model="editForm.managePermissionLevel" style="width: 100%">
            <el-option :value="0" label="跟随访问等级" />
            <el-option v-for="lv in manageOptions" :key="lv" :value="lv" :label="permissionLabel(lv)" />
          </el-select>
          <div class="form-tip">管理该桶所需的最低等级;管理要求不得松于访问要求</div>
        </el-form-item>
        <el-form-item label="配额">
          <el-input-number v-model="editForm.quota" :min="0" :step="1073741824" style="width: 100%" />
          <div class="form-tip">单位:字节;0 = 不限容量</div>
        </el-form-item>
        <el-form-item label="状态">
          <el-radio-group v-model="editForm.status">
            <el-radio :value="1">正常</el-radio>
            <el-radio :value="0">禁用</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="editing" @click="onEdit">保存</el-button>
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
  margin-left: auto;
  font-size: 13px;
  color: #909399;
}

.error-box {
  display: flex;
  justify-content: center;
}

.bucket-icon {
  margin-right: 6px;
  vertical-align: -2px;
}

.form-tip {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
  margin-top: 4px;
}
</style>
