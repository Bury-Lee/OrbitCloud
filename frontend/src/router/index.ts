// 路由:登录页 + 403 页 + 应用页面(桶列表 / 文件 / 分享 / 我的组 / 管理页)
// 守卫链:未登录 → 登录页(带 redirect);requiresAdmin 且非管理员 → 403 页
import { createRouter, createWebHistory } from 'vue-router'

import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/LoginView.vue'),
      meta: { title: '登录' },
    },
    {
      path: '/403',
      name: 'forbidden',
      component: () => import('@/views/ForbiddenView.vue'),
      meta: { title: '无权限' },
    },
    {
      // 公开分享落地页(无需登录;需要提取码时页内弹输入框)
      path: '/share/:token',
      name: 'share',
      component: () => import('@/views/ShareView.vue'),
      meta: { title: '分享' },
    },
    {
      path: '/',
      name: 'buckets',
      component: () => import('@/views/BucketsView.vue'),
      meta: { title: '我的网盘', requiresAuth: true },
    },
    {
      path: '/buckets/:id',
      name: 'files',
      component: () => import('@/views/FilesView.vue'),
      meta: { title: '文件管理', requiresAuth: true },
    },
    {
      path: '/shares',
      name: 'shares',
      component: () => import('@/views/SharesView.vue'),
      meta: { title: '分享管理', requiresAuth: true },
    },
    {
      path: '/groups',
      name: 'my-groups',
      component: () => import('@/views/MyGroupsView.vue'),
      meta: { title: '我的组', requiresAuth: true },
    },
    {
      path: '/admin/users',
      name: 'users',
      component: () => import('@/views/UsersView.vue'),
      meta: { title: '用户管理', requiresAuth: true, requiresAdmin: true },
    },
    {
      path: '/admin/groups',
      name: 'groups',
      component: () => import('@/views/GroupsView.vue'),
      meta: { title: '组管理', requiresAuth: true, requiresAdmin: true },
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

// 全局守卫:未登录访问受保护页面 → 跳登录(带 redirect 回跳);管理员路由非管理员 → 403
router.beforeEach((to) => {
  const auth = useAuthStore()
  if (to.meta.requiresAuth && !auth.isLoggedIn) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.meta.requiresAdmin && !auth.isAdmin) {
    return { name: 'forbidden' }
  }
  if (to.name === 'login' && auth.isLoggedIn) {
    return { path: '/' }
  }
  document.title = to.meta.title ? `${to.meta.title} · OrbitCloud` : 'OrbitCloud'
  return true
})

export default router
