<script setup lang="ts">
// 用户管理页(管理员):元素 = 工具栏(新建/刷新) + 用户表格 + 弹窗(新建/编辑)
// 操作归属:新建/编辑 → 弹窗;删除 → 确认后刷新
import { Delete, Edit, Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { onMounted, reactive, ref } from 'vue'

import type { User } from '@/api/types'
import { formatTime, permissionLabel } from '@/api/types'
import { deleteUser, listUsers, registerUser, updateUser } from '@/api/users'
import AppLayout from '@/components/AppLayout.vue'
import { useDialog } from '@/composables/useDialog'
import { useListState } from '@/composables/useListState'

const dialog = useDialog(['create-user', 'edit-user'])
const createDialog = dialog.model('create-user')
const editDialog = dialog.model('edit-user')
const { loading, error, run } = useListState()

const users = ref<User[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)

async function load() {
  const res = await run(
    () => listUsers({ page: page.value, page_size: pageSize.value }),
    (m) => ElMessage.error(m),
  )
  if (res) {
    users.value = res.items
    total.value = res.total
  }
}

function onPageChange(p: number) {
  page.value = p
  load()
}

// 可选权限级别:0(超级管理员)只能命令行创建,这里提供 1-3
const permOptions = Array.from({ length: 3 }, (_, i) => i + 1)

// ---- 新建用户(register) ----
const createFormRef = ref<FormInstance>()
const creating = ref(false)
const createForm = reactive({ username: '', password: '', permissionLevel: 3 })

const createRules: FormRules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 8, message: '密码至少 8 位', trigger: 'blur' },
  ],
}

async function onCreate() {
  if (!createFormRef.value) return
  const valid = await createFormRef.value.validate().catch(() => false)
  if (!valid) return
  creating.value = true
  try {
    await registerUser({
      username: createForm.username,
      password: createForm.password,
      permission_level: createForm.permissionLevel,
    })
    ElMessage.success('创建成功')
    dialog.close()
    createForm.username = ''
    createForm.password = ''
    createForm.permissionLevel = 3
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '创建失败')
  } finally {
    creating.value = false
  }
}

// ---- 编辑用户 ----
const editing = ref(false)
const editForm = reactive({
  id: 0,
  name: '',
  email: '',
  permissionLevel: 1,
  status: 1,
  password: '',
})

function openEdit(row: User) {
  editForm.id = row.ID
  editForm.name = row.Name
  editForm.email = row.Email
  editForm.permissionLevel = row.PermissionLevel
  editForm.status = row.Status
  editForm.password = ''
  dialog.open('edit-user')
}

async function onEdit() {
  editing.value = true
  try {
    await updateUser(editForm.id, {
      name: editForm.name,
      email: editForm.email,
      permission_level: editForm.permissionLevel,
      status: editForm.status,
      // 密码留空不更新(后端指针字段 nil 语义)
      ...(editForm.password ? { password: editForm.password } : {}),
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

// ---- 删除用户 ----
async function onDelete(row: User) {
  try {
    await ElMessageBox.confirm(
      `删除用户「${row.Username}」?该操作不可恢复。`,
      '删除用户',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    await deleteUser(row.ID)
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
      <el-button type="primary" :icon="Plus" @click="dialog.open('create-user')">新建用户</el-button>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <span class="hint">权限数值越小权限越高;0(超级管理员)仅能由后端命令行创建</span>
    </div>

    <div v-if="error" class="error-box">
      <el-result icon="error" :title="`加载失败:${error}`">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>
    </div>

    <el-table v-else v-loading="loading" :data="users" stripe style="width: 100%">
      <el-table-column prop="ID" label="ID" width="70" />
      <el-table-column prop="Username" label="用户名" min-width="140" />
      <el-table-column prop="Name" label="姓名" min-width="120" />
      <el-table-column prop="Email" label="邮箱" min-width="160" show-overflow-tooltip />
      <el-table-column label="权限" width="140">
        <template #default="{ row }">
          <el-tag size="small" :type="row.PermissionLevel <= 1 ? 'danger' : 'primary'" effect="plain">
            {{ permissionLabel(row.PermissionLevel) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.Status === 1 ? 'success' : 'danger'">
            {{ row.Status === 1 ? '正常' : '禁用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近登录" width="170">
        <template #default="{ row }">{{ formatTime(row.LastLogin) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="160" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" :icon="Edit" @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" size="small" :icon="Delete" @click="onDelete(row)">删除</el-button>
        </template>
      </el-table-column>
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

    <!-- 新建用户 -->
    <el-dialog v-model="createDialog" title="新建用户" width="460px">
      <el-form ref="createFormRef" :model="createForm" :rules="createRules" label-width="90px">
        <el-form-item label="用户名" prop="username">
          <el-input v-model="createForm.username" maxlength="64" />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input v-model="createForm.password" type="password" show-password maxlength="64" />
        </el-form-item>
        <el-form-item label="权限">
          <el-select v-model="createForm.permissionLevel" style="width: 100%">
            <el-option
              v-for="p in permOptions"
              :key="p"
              :value="p"
              :label="`${p} · ${permissionLabel(p)}`"
            />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="creating" @click="onCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- 编辑用户 -->
    <el-dialog v-model="editDialog" title="编辑用户" width="460px">
      <el-form label-width="90px">
        <el-form-item label="姓名">
          <el-input v-model="editForm.name" maxlength="64" />
        </el-form-item>
        <el-form-item label="邮箱">
          <el-input v-model="editForm.email" maxlength="128" />
        </el-form-item>
        <el-form-item label="权限">
          <el-select v-model="editForm.permissionLevel" style="width: 100%">
            <el-option
              v-for="p in permOptions"
              :key="p"
              :value="p"
              :label="`${p} · ${permissionLabel(p)}`"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="状态">
          <el-radio-group v-model="editForm.status">
            <el-radio :value="1">正常</el-radio>
            <el-radio :value="0">禁用</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="重置密码">
          <el-input
            v-model="editForm.password"
            type="password"
            show-password
            placeholder="留空则不修改密码"
            maxlength="64"
          />
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

.pager {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}
</style>
