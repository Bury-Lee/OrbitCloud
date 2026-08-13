// 认证相关 API:登录 / 刷新 / 登出
import http from './http'
import type { User } from './types'

export interface LoginResult {
  access_token: string
  /** 访问令牌有效期(秒) */
  expires_in: number
  refresh_token: string
  /** 当前用户信息(已脱敏) */
  user: User
}

export interface RefreshResult {
  access_token: string
  expires_in: number
  refresh_token: string
  user: User
}

/** POST /auth/login 登录(签发令牌对) */
export function login(username: string, password: string): Promise<LoginResult> {
  return http.post('/auth/login', { username, password })
}

/** POST /auth/refresh 刷新令牌(轮换,旧 refresh 即刻失效) */
export function refresh(refreshToken: string): Promise<RefreshResult> {
  return http.post('/auth/refresh', { refresh_token: refreshToken })
}

/** POST /auth/logout 登出(吊销刷新令牌;幂等成功) */
export function logout(refreshToken: string): Promise<void> {
  return http.post('/auth/logout', { refresh_token: refreshToken })
}
