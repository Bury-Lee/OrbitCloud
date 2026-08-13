// 认证状态(Pinia):令牌持久化(localStorage)与用户信息 + 显式会话状态机
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { login as apiLogin, logout as apiLogout } from '@/api/auth'
import { REFRESH_TOKEN_KEY, TOKEN_KEY } from '@/api/http'
import type { User } from '@/api/types'

const USER_KEY = 'orbitcloud_user'

/** 会话状态:boot(启动待定) / authed(已登录) / expired(会话过期,凭证已清) */
export type SessionState = 'boot' | 'authed' | 'expired'

function loadUser(): User | null {
  try {
    const raw = localStorage.getItem(USER_KEY)
    return raw ? (JSON.parse(raw) as User) : null
  } catch {
    return null
  }
}

export const useAuthStore = defineStore('auth', () => {
  const accessToken = ref<string | null>(localStorage.getItem(TOKEN_KEY))
  const refreshToken = ref<string | null>(localStorage.getItem(REFRESH_TOKEN_KEY))
  const username = ref<string | null>(localStorage.getItem('orbitcloud_username'))
  const user = ref<User | null>(loadUser())
  const sessionState = ref<SessionState>(accessToken.value ? 'authed' : 'boot')

  const isLoggedIn = computed(() => Boolean(accessToken.value))
  /** 是否管理员(后端约定:PermissionLevel <= 1,见 server/user.go) */
  const isAdmin = computed(() => (user.value?.PermissionLevel ?? 99) <= 1)

  interface Tokens {
    access_token: string
    refresh_token: string
  }

  function persist(tokens: Tokens, u?: User) {
    accessToken.value = tokens.access_token
    refreshToken.value = tokens.refresh_token
    sessionState.value = 'authed'
    localStorage.setItem(TOKEN_KEY, tokens.access_token)
    localStorage.setItem(REFRESH_TOKEN_KEY, tokens.refresh_token)
    if (u) {
      user.value = u
      username.value = u.Username
      localStorage.setItem(USER_KEY, JSON.stringify(u))
      localStorage.setItem('orbitcloud_username', u.Username)
    }
  }

  async function login(usernameInput: string, password: string) {
    const data = await apiLogin(usernameInput, password)
    persist(data, data.user)
    return data
  }

  async function logout() {
    if (refreshToken.value) {
      try {
        await apiLogout(refreshToken.value)
      } catch {
        // 登出接口失败不阻塞本地清理
      }
    }
    clear()
  }

  /** 清空本地凭证与状态(登出 / 会话过期共用) */
  function clear() {
    accessToken.value = null
    refreshToken.value = null
    username.value = null
    user.value = null
    localStorage.removeItem(TOKEN_KEY)
    localStorage.removeItem(REFRESH_TOKEN_KEY)
    localStorage.removeItem(USER_KEY)
    localStorage.removeItem('orbitcloud_username')
  }

  /** 会话过期:由 http.ts 401 刷新失败回调触发(main.ts 注册),随后路由跳登录 */
  function expireSession() {
    clear()
    sessionState.value = 'expired'
  }

  return {
    accessToken,
    refreshToken,
    username,
    user,
    sessionState,
    isLoggedIn,
    isAdmin,
    login,
    logout,
    clear,
    expireSession,
    persist,
  }
})
