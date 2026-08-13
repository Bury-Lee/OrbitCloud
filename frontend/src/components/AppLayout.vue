<script setup lang="ts">
import { FolderOpened, Link, SwitchButton, User, UserFilled } from '@element-plus/icons-vue'
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

// 子路由(/buckets/:id)时仍高亮"我的网盘"
const activeMenu = computed(() => (route.path.startsWith('/buckets') ? '/' : route.path))

async function onLogout() {
  await auth.logout()
  router.push('/login')
}
</script>

<template>
  <el-container class="layout">
    <el-aside width="210px" class="aside">
      <div class="brand">
        <img src="/favicon.svg" alt="logo" class="logo" />
        <span class="brand-text">OrbitCloud</span>
      </div>
      <el-menu :default-active="activeMenu" router class="menu">
        <el-menu-item index="/">
          <el-icon><FolderOpened /></el-icon>
          <span>我的网盘</span>
        </el-menu-item>
        <el-menu-item index="/shares">
          <el-icon><Link /></el-icon>
          <span>分享管理</span>
        </el-menu-item>
        <el-menu-item index="/groups">
          <el-icon><UserFilled /></el-icon>
          <span>我的组</span>
        </el-menu-item>
        <el-menu-item v-if="auth.isAdmin" index="/admin/users">
          <el-icon><User /></el-icon>
          <span>用户管理</span>
        </el-menu-item>
        <el-menu-item v-if="auth.isAdmin" index="/admin/groups">
          <el-icon><UserFilled /></el-icon>
          <span>组管理</span>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container class="body">
      <el-header class="header">
        <div class="page-title">{{ route.meta.title ?? 'OrbitCloud' }}</div>
        <div class="right">
          <el-tag v-if="auth.username" type="success" effect="plain">{{ auth.username }}</el-tag>
          <el-button text type="primary" :icon="SwitchButton" @click="onLogout">退出</el-button>
        </div>
      </el-header>
      <el-main class="main">
        <slot />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.layout {
  height: 100%;
}

.aside {
  display: flex;
  flex-direction: column;
  background: #fff;
  border-right: 1px solid #e4e7ed;
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 16px;
  font-size: 17px;
  font-weight: 600;
  border-bottom: 1px solid #f0f2f5;
}

.logo {
  width: 28px;
  height: 28px;
}

.menu {
  flex: 1;
  border-right: none;
}

.body {
  min-width: 0;
}

.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #fff;
  border-bottom: 1px solid #e4e7ed;
}

.page-title {
  font-size: 16px;
  font-weight: 600;
}

.right {
  display: flex;
  align-items: center;
  gap: 12px;
}

.main {
  overflow: auto;
}
</style>
