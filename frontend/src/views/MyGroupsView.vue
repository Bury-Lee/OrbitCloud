<script setup lang="ts">
// 我的组页(只读):元素 = 工具栏(提示+刷新) + 只读表格
// 操作归属:仅刷新;无写操作、无操作列、无跳转出口(后端接口即只读)
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { onMounted, ref } from 'vue'

import { listMyGroups } from '@/api/groups'
import type { UserGroup } from '@/api/types'
import { formatTime } from '@/api/types'
import AppLayout from '@/components/AppLayout.vue'
import { useListState } from '@/composables/useListState'

const { loading, error, run } = useListState()
const groups = ref<UserGroup[]>([])

async function load() {
  groups.value = (await run(listMyGroups, (m) => ElMessage.error(m))) ?? groups.value
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="toolbar">
      <span class="hint">我加入的用户组;文件/文件夹可设为"仅组内成员可见"</span>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
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
      <el-table-column prop="Name" label="组名" min-width="160" />
      <el-table-column prop="Description" label="描述" min-width="240" show-overflow-tooltip />
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ formatTime(row.CreatedAt) }}</template>
      </el-table-column>
      <template #empty>
        <el-empty description="还没有加入任何组" :image-size="80" />
      </template>
    </el-table>
  </AppLayout>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  margin-bottom: 14px;
}

.hint {
  margin-right: auto;
  font-size: 13px;
  color: #909399;
}

.error-box {
  display: flex;
  justify-content: center;
}
</style>
