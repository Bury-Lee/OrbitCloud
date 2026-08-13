// 桶模块 API —— 创建 / 列表 / 详情 / 修改 / 删除
import http from './http'
import type { Bucket } from './types'

/** GET /buckets 可见桶列表(本人创建或权限足够) */
export function listBuckets(): Promise<Bucket[]> {
  return http.get('/buckets')
}

/** GET /buckets/:id 桶详情 */
export function getBucket(id: number): Promise<Bucket> {
  return http.get(`/buckets/${id}`)
}

/**
 * POST /buckets 创建桶。
 * 后端语义:permission_level 缺省/<=0 时自动取创建者等级(仅允许创建相同等级的桶),
 * 因此前端不传该字段即可。
 */
export function createBucket(data: { name: string; description?: string }): Promise<Bucket> {
  return http.post('/buckets', data)
}

/** PUT /buckets/:id 修改桶(仅传入需要更新的字段) */
export function updateBucket(
  id: number,
  data: { description?: string; permission_level?: number; quota?: number; status?: number },
): Promise<Bucket> {
  return http.put(`/buckets/${id}`, data)
}

/** DELETE /buckets/:id 删除桶(后端级联删除桶内全部文件,经删除任务表异步执行) */
export function deleteBucket(id: number): Promise<void> {
  return http.delete(`/buckets/${id}`)
}
