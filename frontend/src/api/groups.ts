// 用户组模块 API —— 组 CRUD / 成员管理 / 我的组 / 条目可见组设置
import http from './http'
import type { GroupMember, PageResult, UserGroup } from './types'

/** 创建用户组入参(组 = 纯可见组白名单,无权限等级字段,2026-08-12 审批) */
export interface CreateGroupArg {
  name: string
  description?: string
}

/** POST /groups 创建组(管理员) */
export function createGroup(data: CreateGroupArg): Promise<UserGroup> {
  return http.post('/groups', data)
}

/** GET /groups?page=&page_size= 组列表(管理员看全部;普通用户仅自己所在组) */
export function listGroups(params: { page?: number; page_size?: number } = {}): Promise<PageResult<UserGroup>> {
  return http.get('/groups', { params })
}

/** GET /groups/:id 组详情(管理员或组内成员) */
export function getGroup(id: number): Promise<UserGroup> {
  return http.get(`/groups/${id}`)
}

/** PUT /groups/:id 更新组(管理员;仅传入需更新的字段) */
export function updateGroup(
  id: number,
  data: { name?: string; description?: string; status?: number },
): Promise<UserGroup> {
  return http.put(`/groups/${id}`, data)
}

/** DELETE /groups/:id 删除组(管理员,软删) */
export function deleteGroup(id: number): Promise<void> {
  return http.delete(`/groups/${id}`)
}

/** POST /groups/:id/members 添加成员(管理员) */
export function addGroupMember(id: number, user_id: number): Promise<{ group_id: number; user_id: number }> {
  return http.post(`/groups/${id}/members`, { user_id })
}

/** DELETE /groups/:id/members/:uid 移除成员(管理员) */
export function removeGroupMember(id: number, uid: number): Promise<void> {
  return http.delete(`/groups/${id}/members/${uid}`)
}

/** GET /groups/:id/members?page=&page_size= 成员列表(管理员或组内成员,含用户名) */
export function listGroupMembers(
  id: number,
  params: { page?: number; page_size?: number } = {},
): Promise<PageResult<GroupMember>> {
  return http.get(`/groups/${id}/members`, { params })
}

/** GET /users/me/groups 我的组(任意登录用户,仅正常组) */
export function listMyGroups(): Promise<UserGroup[]> {
  return http.get('/users/me/groups')
}

/**
 * PUT /buckets/:id/files/:fid/visibility 设置文件可见组(创建者或管理员)。
 * @param groups 可见组 ID 列表;空数组 = 恢复不限制(按桶权限)
 */
export function setFileVisibility(bucketId: number, fileId: number, groups: number[]): Promise<unknown> {
  return http.put(`/buckets/${bucketId}/files/${fileId}/visibility`, { groups })
}

/**
 * PUT /buckets/:id/dirs/:fid/visibility 设置文件夹可见组(创建者或管理员;仅本目录行,不递归子树)。
 * @param groups 可见组 ID 列表;空数组 = 恢复不限制(按桶权限)
 */
export function setFolderVisibility(bucketId: number, dirId: number, groups: number[]): Promise<unknown> {
  return http.put(`/buckets/${bucketId}/dirs/${dirId}/visibility`, { groups })
}
