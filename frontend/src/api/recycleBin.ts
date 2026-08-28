// 回收站模块 API —— 列表(文件/文件夹)/ 永久删除 / 恢复
// 后端返回统一分页结构(common.PaginatedResult):{list, total, page, page_size, total_pages, has_more, next_cursor}
import http from './http'
import type { FileItem, FolderItem } from './types'

/** 回收站分页响应(通用分页结构) */
export interface CommonPageResult<T> {
  list: T[]
  total: number
  page: number
  page_size: number
  total_pages: number
  has_more: boolean
  next_cursor: number
}

/** 回收站查询参数(user_id 缺省 = 自己;查他人仅管理员) */
export interface RecycleQuery {
  page?: number
  page_size?: number
  cursor?: number
  key?: string
  order?: string
  user_id?: number
}

/** GET /recycle-bin/files?page=&page_size=&cursor=&key=&order=&user_id= 回收站文件列表 */
export function listRecycleFiles(params: RecycleQuery = {}): Promise<CommonPageResult<FileItem>> {
  return http.get('/recycle-bin/files', { params })
}

/** GET /recycle-bin/folders?page=&page_size=&cursor=&key=&order=&user_id= 回收站文件夹列表 */
export function listRecycleFolders(params: RecycleQuery = {}): Promise<CommonPageResult<FolderItem>> {
  return http.get('/recycle-bin/folders', { params })
}

/** DELETE /recycle-bin/files/:id 永久删除回收站文件(提前清除,204;不可恢复) */
export function deleteRecycleFile(fileId: number): Promise<void> {
  return http.delete(`/recycle-bin/files/${fileId}`)
}

/** POST /recycle-bin/files/:id/restore 恢复回收站文件(返回恢复后的文件记录;可能重命名/回桶根) */
export function restoreRecycleFile(fileId: number): Promise<FileItem> {
  return http.post(`/recycle-bin/files/${fileId}/restore`)
}
