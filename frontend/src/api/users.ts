// 用户模块 API —— 当前用户 / 列表 / 修改 / 删除 / 注册(管理员)
import http from './http'
import type { PageResult, User } from './types'

/** GET /users/me 当前用户信息(基于 JWT Claims,不经 URL 参数) */
export function me(): Promise<User> {
  return http.get('/users/me')
}

/** PUT /users/me 修改本人(仅 Password/Name/Email 可改,不可自提权/自禁) */
export function updateMe(data: { password?: string; name?: string; email?: string }): Promise<User> {
  return http.put('/users/me', data)
}

/** GET /users?page=&page_size= 用户列表(管理员专属) */
export function listUsers(params: { page?: number; page_size?: number } = {}): Promise<PageResult<User>> {
  return http.get('/users', { params })
}

/** PUT /users/:id 管理员修改用户(权限/状态/改名/重置密码) */
export function updateUser(
  id: number,
  data: { password?: string; name?: string; email?: string; permission_level?: number; status?: number },
): Promise<User> {
  return http.put(`/users/${id}`, data)
}

/** DELETE /users/:id 删除用户(管理员专属;同级别管理员不可删) */
export function deleteUser(id: number): Promise<void> {
  return http.delete(`/users/${id}`)
}

/** POST /auth/register 注册新用户(管理员专属;普通用户不能自助注册) */
export function registerUser(data: { username: string; password: string; permission_level?: number }): Promise<User> {
  return http.post('/auth/register', data)
}
