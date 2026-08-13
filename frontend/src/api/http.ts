// HTTP 客户端封装:axios 实例 + JWT 注入 + 统一解包/错误处理 + 401 自动刷新令牌轮换
import axios, {
  type AxiosError,
  type AxiosResponse,
  type InternalAxiosRequestConfig,
} from 'axios'

// 后端统一响应结构(见后端 common/response.go)
export interface ApiResponse<T = unknown> {
  code: number
  data?: T
  message: string
}

export const TOKEN_KEY = 'orbitcloud_access_token'
export const REFRESH_TOKEN_KEY = 'orbitcloud_refresh_token'

export const http = axios.create({
  // 开发期经 Vite 代理(/api → http://127.0.0.1:8080);生产由 Nginx 同源反代
  baseURL: '/api/v1',
  timeout: 30000,
})

// 请求拦截:注入 Authorization: Bearer <token>
http.interceptors.request.use((config) => {
  const token = localStorage.getItem(TOKEN_KEY)
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// ---- 会话过期回调(替代 location.href 硬跳) ----
// 401 刷新失败后调用,由 main.ts 注册:清凭证(store) + 路由跳登录(带 redirect 回跳)。
// 用回调而非直接 import store,避免 api → store → api 循环依赖。
let sessionExpiredHandler: (() => void) | null = null

export function registerSessionExpiredHandler(fn: () => void) {
  sessionExpiredHandler = fn
}

// ---- 刷新令牌轮换 ----
// 401 时先尝试用 refresh_token 换新令牌对(后端为轮换语义,旧 refresh 即刻失效),
// 成功则重放原请求;失败才清凭证跳登录。并发 401 共享同一个刷新 Promise,避免重复刷新。
let refreshing: Promise<{ access_token: string; refresh_token: string }> | null = null

async function tryRefreshToken(): Promise<boolean> {
  const rt = localStorage.getItem(REFRESH_TOKEN_KEY)
  if (!rt) return false
  if (!refreshing) {
    // 不走 http 实例,避免与响应拦截器互相递归
    refreshing = axios
      .post<ApiResponse<{ access_token: string; refresh_token: string }>>('/api/v1/auth/refresh', {
        refresh_token: rt,
      })
      .then((resp) => {
        const data = resp.data?.data
        if (!data || !data.access_token) {
          throw new Error('refresh response missing token')
        }
        localStorage.setItem(TOKEN_KEY, data.access_token)
        localStorage.setItem(REFRESH_TOKEN_KEY, data.refresh_token)
        return data
      })
      .finally(() => {
        refreshing = null
      })
  }
  try {
    await refreshing
    return true
  } catch {
    return false
  }
}

// 请求配置扩展标记:已尝试过刷新,避免 401 死循环
type RetriableConfig = InternalAxiosRequestConfig & { _retried?: boolean }

// 响应拦截:HTTP 2xx 时解包返回 data;业务 code!=0 / 非 2xx 统一 reject;
// 401 先尝试刷新令牌重放,失败走会话过期回调(store 清凭证 + 路由跳登录)
http.interceptors.response.use(
  (resp: AxiosResponse<ApiResponse>) => {
    const body = resp.data
    // 204(如 logout)无响应体,直接成功
    if (resp.status === 204) return undefined as never
    if (body && body.code === 0) return body.data as never
    return Promise.reject(new Error(body?.message || '请求失败'))
  },
  async (error: AxiosError<ApiResponse>) => {
    const status = error.response?.status
    const config = error.config as RetriableConfig | undefined
    const message = error.response?.data?.message || error.message || '网络错误'

    // 401 且未重试过:尝试刷新令牌后重放原请求
    if (status === 401 && config && !config._retried) {
      config._retried = true
      if (await tryRefreshToken()) {
        const token = localStorage.getItem(TOKEN_KEY)
        if (token) {
          config.headers.Authorization = `Bearer ${token}`
        }
        return http(config)
      }
    }

    // 仅 401(刷新失败/无可刷新令牌)才判会话过期:清空凭证并触发回调。
    // 其它状态码(404/403/500 等)只 reject,绝不清凭证——否则一次业务错误
    // (如枚举不存在的目录)会把用户整个踢回登录页,后续请求全部 401。
    if (status === 401) {
      localStorage.removeItem(TOKEN_KEY)
      localStorage.removeItem(REFRESH_TOKEN_KEY)
      sessionExpiredHandler?.()
    }
    return Promise.reject(new Error(message))
  },
)

export default http
