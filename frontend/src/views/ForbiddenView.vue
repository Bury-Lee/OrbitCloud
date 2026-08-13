<script setup lang="ts">
// 403 无权限页:非管理员访问 requiresAdmin 路由时进入(路由守卫重定向)
import { useRouter } from 'vue-router'

const router = useRouter()

function backHome() {
  router.push('/')
}
</script>

<template>
  <div class="forbidden-page">
    <el-result icon="warning" title="403 无权限" sub-title="当前账号无权访问该页面,请联系管理员">
      <template #extra>
        <el-button type="primary" @click="backHome">返回首页</el-button>
      </template>
    </el-result>
  </div>
</template>

<style scoped>
.forbidden-page {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #f5f7fa;
}
</style>
