<script setup lang="ts">
// 组管理页(管理员):元素 = 工具栏(新建/刷新) + 组表格 + 弹窗(新建/编辑 + 成员管理)
// 操作归属:新建/编辑/成员 → 弹窗;删除 → 确认后刷新
// 唯一允许的二级弹窗:成员管理 → 添加成员(打开时主弹窗"members"保持打开)
import { Delete, Edit, Plus, Refresh, UserFilled } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { onMounted, reactive, ref } from 'vue'

import {
  addGroupMember,
  createGroup,
  deleteGroup,
  listGroupMembers,
  listGroups,
  removeGroupMember,
  updateGroup,
} from '@/api/groups'
import type { GroupMember, User, UserGroup } from '@/api/types'
import { formatTime } from '@/api/types'
import { listUsers } from '@/api/users'
import AppLayout from '@/components/AppLayout.vue'
import { useDialog } from '@/composables/useDialog'
import { useListState } from '@/composables/useListState'

const dialog = useDialog(['edit-group', 'members'])
const editGroupDialog = dialog.model('edit-group')
const membersDialog = dialog.model('members')
const { loading, error, run } = useListState()

const groups = ref<UserGroup[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)

async function load() {
  const res = await run(
    () => listGroups({ page: page.value, page_size: pageSize.value }),
    (m) => ElMessage.error(m),
  )
  if (res) {
    groups.value = res.items
    total.value = res.total
  }
}

function onPageChange(p: number) {
  page.value = p
  load()
}

// ---- 新建 / 编辑组 ----
const editing = ref(false)
const editFormRef = ref<FormInstance>()
const editForm = reactive({
  id: 0,
  name: '',
  description: '',
  status: 1,
})
const isCreate = () => editForm.id === 0

const editRules: FormRules = {
  name: [{ required: true, message: '请输入组名', trigger: 'blur' }],
}

function openCreate() {
  editForm.id = 0
  editForm.name = ''
  editForm.description = ''
  editForm.status = 1
  dialog.open('edit-group')
}

function openEdit(row: UserGroup) {
  editForm.id = row.ID
  editForm.name = row.Name
  editForm.description = row.Description
  editForm.status = row.Status
  dialog.open('edit-group')
}

async function onSave() {
  if (!editFormRef.value) return
  const valid = await editFormRef.value.validate().catch(() => false)
  if (!valid) return
  editing.value = true
  try {
    if (isCreate()) {
      await createGroup({
        name: editForm.name,
        description: editForm.description,
      })
      ElMessage.success('创建成功')
    } else {
      await updateGroup(editForm.id, {
        name: editForm.name,
        description: editForm.description,
        status: editForm.status,
      })
      ElMessage.success('已保存')
    }
    dialog.close()
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    editing.value = false
  }
}

// ---- 删除组 ----
async function onDelete(row: UserGroup) {
  try {
    await ElMessageBox.confirm(
      `删除组「${row.Name}」?组被删除后,设为该组可见的文件/文件夹将不再对原组员开放。`,
      '删除组',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    await deleteGroup(row.ID)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

// ---- 成员管理(主弹窗 members 内的二级弹窗 add-member) ----
const memberGroup = ref<UserGroup | null>(null)
const memberLoading = ref(false)
const members = ref<GroupMember[]>([])
const memberTotal = ref(0)
const memberPage = ref(1)
const memberPageSize = ref(50)

async function loadMembers() {
  const g = memberGroup.value
  if (!g) return
  memberLoading.value = true
  try {
    const res = await listGroupMembers(g.ID, { page: memberPage.value, page_size: memberPageSize.value })
    members.value = res.items
    memberTotal.value = res.total
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '加载成员失败')
  } finally {
    memberLoading.value = false
  }
}

function openMembers(row: UserGroup) {
  memberGroup.value = row
  members.value = []
  memberTotal.value = 0
  memberPage.value = 1
  dialog.open('members')
  loadMembers()
}

function onMemberPageChange(p: number) {
  memberPage.value = p
  loadMembers()
}

// ---- 添加成员(二级弹窗) ----
const addMemberVisible = ref(false)
const userOptions = ref<User[]>([])
const addUserID = ref<number | null>(null)
const adding = ref(false)

async function openAddMember() {
  addUserID.value = null
  try {
    // 拉取全部用户(分页 50 步进),供选择;量级过大可后续改远程搜索
    const all: User[] = []
    let p = 1
    for (;;) {
      const res = await listUsers({ page: p, page_size: 50 })
      all.push(...res.items)
      if (p * 50 >= res.total) break
      p++
    }
    userOptions.value = all
  } catch {
    userOptions.value = []
  }
  addMemberVisible.value = true
}

async function onAddMember() {
  const g = memberGroup.value
  if (!g || !addUserID.value) {
    ElMessage.warning('请选择用户')
    return
  }
  adding.value = true
  try {
    await addGroupMember(g.ID, addUserID.value)
    ElMessage.success('已添加')
    addMemberVisible.value = false
    await loadMembers()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '添加失败')
  } finally {
    adding.value = false
  }
}

// ---- 移除成员 ----
async function onRemoveMember(m: GroupMember) {
  const g = memberGroup.value
  if (!g) return
  try {
    await ElMessageBox.confirm(`将用户「${m.Username}」移出组「${g.Name}」?`, '移除成员', {
      type: 'warning',
      confirmButtonText: '移除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await removeGroupMember(g.ID, m.UserID)
    ElMessage.success('已移除')
    await loadMembers()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '移除失败')
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="toolbar">
      <el-button type="primary" :icon="Plus" @click="openCreate">新建组</el-button>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <span class="hint">组用于文件/文件夹"仅组内成员可见";组与权限等级彼此隔离,加入组不改变自身权限</span>
    </div>

    <div v-if="error" class="error-box">
      <el-result icon="error" :title="`加载失败:${error}`">
        <template #extra>
          <el-button type="primary" @click="load">重试</el-button>
        </template>
      </el-result>
    </div>

    <el-table v-else v-loading="loading" :data="groups" stripe style="width: 100%">
      <el-table-column prop="ID" label="ID" width="70" />
      <el-table-column prop="Name" label="组名" min-width="140" />
      <el-table-column prop="Description" label="描述" min-width="200" show-overflow-tooltip />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.Status === 1 ? 'success' : 'danger'">
            {{ row.Status === 1 ? '正常' : '禁用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ formatTime(row.CreatedAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="200" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" :icon="UserFilled" @click="openMembers(row)">
            成员
          </el-button>
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

    <!-- 新建 / 编辑组 -->
    <el-dialog v-model="editGroupDialog" :title="isCreate() ? '新建组' : '编辑组'" width="460px">
      <el-form ref="editFormRef" :model="editForm" :rules="editRules" label-width="90px">
        <el-form-item label="组名" prop="name">
          <el-input v-model="editForm.name" maxlength="64" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="editForm.description" maxlength="255" type="textarea" :rows="2" />
        </el-form-item>
        <el-form-item v-if="!isCreate()" label="状态">
          <el-radio-group v-model="editForm.status">
            <el-radio :value="1">正常</el-radio>
            <el-radio :value="0">禁用</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.close()">取消</el-button>
        <el-button type="primary" :loading="editing" @click="onSave">{{ isCreate() ? '创建' : '保存' }}</el-button>
      </template>
    </el-dialog>

    <!-- 成员管理 -->
    <el-dialog v-model="membersDialog" :title="`成员管理 · ${memberGroup?.Name ?? ''}`" width="640px">
      <div class="member-toolbar">
        <el-button type="primary" size="small" :icon="Plus" @click="openAddMember">添加成员</el-button>
      </div>
      <el-table v-loading="memberLoading" :data="members" stripe style="width: 100%">
        <el-table-column prop="ID" label="ID" width="70" />
        <el-table-column prop="Username" label="用户名" min-width="140" />
        <el-table-column prop="Name" label="姓名" min-width="120" />
        <el-table-column label="加入时间" width="170">
          <template #default="{ row }">{{ formatTime(row.CreatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button link type="danger" size="small" :icon="Delete" @click="onRemoveMember(row)">
              移除
            </el-button>
          </template>
        </el-table-column>
        <template #empty>
          <el-empty description="暂无成员" :image-size="60" />
        </template>
      </el-table>
      <div class="pager">
        <el-pagination
          v-model:current-page="memberPage"
          :page-size="memberPageSize"
          :total="memberTotal"
          layout="total, prev, pager, next"
          @current-change="onMemberPageChange"
        />
      </div>
    </el-dialog>

    <!-- 添加成员(二级弹窗,唯一允许的嵌套) -->
    <el-dialog v-model="addMemberVisible" title="添加成员" width="460px">
      <el-select v-model="addUserID" filterable placeholder="选择用户" style="width: 100%">
        <el-option
          v-for="u in userOptions"
          :key="u.ID"
          :value="u.ID"
          :label="u.Username"
        />
      </el-select>
      <template #footer>
        <el-button @click="addMemberVisible = false">取消</el-button>
        <el-button type="primary" :loading="adding" @click="onAddMember">添加</el-button>
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

.member-toolbar {
  margin-bottom: 12px;
}
</style>
