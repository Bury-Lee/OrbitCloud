// 后端实体类型 —— 与 Go model 字段一一对应(JSON 为 Go 字段名驼峰,见 model/*.go)
// 后端时间统一 UTC,JSON 序列化为 RFC3339 字符串。

/** 用户(后端返回前已脱敏:Password 恒为空串) */
export interface User {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  Username: string
  Password: string
  Name: string
  Remarks: string
  Email: string
  /** 权限级别:0 最高,数值越大权限越低(见 model.PermissionLevel) */
  PermissionLevel: number
  LastLogin: string | null
  /** 1 正常 / 0 禁用 */
  Status: number
}

/** 存储桶 */
export interface Bucket {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  Name: string
  Description: string
  PermissionLevel: number
  OwnerID: number
  /** 容量配额(字节;0=不限) */
  Quota: number
  /** 已用空间(字节) */
  UsedSpace: number
  /** 1 正常 / 0 禁用 */
  Status: number
}

/** 文件夹条目的 FileType 值(前端独立常量,与后端双表模型的 Folder 类型对应) */
export const FILE_TYPE_FOLDER = 'Folder'

/**
 * 文件条目(后端 files 表;双表模型,文件与文件夹分表存储)。
 * FolderID=0 = 桶根(虚拟,桶名即根,无实例行)。
 */
export interface FileItem {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  BucketID: number
  /** 所在文件夹 folders.id;0 = 桶根 */
  FolderID: number
  /** 文件名(不含 "/") */
  Name: string
  NameLower: string
  /** 字节 */
  FileSize: number
  /** MIME 类型 */
  FileType: string
  /** 采样 MD5 */
  MD5: string
  UploadedBy: number
  /** 可见组 ID 的 JSON 数组字符串(如 "[1,5]");空串 = 不限制 */
  VisibleToGroups?: string
}

/**
 * 文件夹条目(后端 folders 表;目录树节点,ParentID 自引用)。
 * ParentID=0 = 桶根(虚拟)。
 */
export interface FolderItem {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  /** 可用标记:false = 已删除(不可用不可达;后台物理清理中) */
  Isable: boolean
  BucketID: number
  /** 父目录 folders.id;0 = 桶根 */
  ParentID: number
  Name: string
  NameLower: string
  UploadedBy: number
  /** 可见组 ID 的 JSON 数组字符串(如 "[1,5]");空串 = 不限制 */
  VisibleToGroups?: string
}

/** 列目录结果(双表类型化:{files, folders, total};文件与文件夹分数组返回) */
export interface ListResult {
  files: FileItem[]
  folders: FolderItem[]
  total: number
}

/**
 * 游标分页列目录结果(前端递归下载专用;双列表分别 keyset 游标,不返回 total)。
 * next*_cursor 为空串 = 对应表已取尽;空串回传 = 对应表首页。
 */
export interface CursorListResult {
  files: FileItem[]
  folders: FolderItem[]
  next_files_cursor: string
  next_folders_cursor: string
}

/** 文件夹下载任务(后端 download_tasks 表;记录下载起点文件夹,进度/断点在前端) */
export interface DownloadTask {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  UserID: number
  /** 下载起点文件夹所属桶 */
  BucketID: number
  /** 下载起点文件夹 folders.id(文件夹下载登记) */
  FolderID: number
  /** 路径快照(仅供展示,可能过期) */
  FilePath: string
}

/** 用户组(后端 user_groups 表) */
export interface UserGroup {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  /** 组名(全局唯一,大小写敏感) */
  Name: string
  Description: string
  /** 创建者 users.id */
  CreatedBy: number
  /** 1 正常 / 0 禁用 */
  Status: number
}

/** 组内成员视图(后端 server.MemberInfo:成员行 + 用户基本信息) */
export interface GroupMember {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  GroupID: number
  UserID: number
  Username: string
  Name: string
}

/** 文件/文件夹条目级可见组(visible_to_groups,JSON 数组字符串) */
export type VisibleToGroups = number[]

/** 可见组 JSON 字符串 → 组 ID 数组(空串/NULL = 不限制) */
export function parseVisibleGroups(raw: string | null | undefined): VisibleToGroups {
  if (!raw) return []
  try {
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? arr.map(Number) : []
  } catch {
    return []
  }
}

/** 组 ID 数组 → visible_to_groups 字符串(空数组 = 不限制) */
export function stringifyVisibleGroups(groups: VisibleToGroups): string {
  return JSON.stringify(groups)
}

/** 文件/文件夹统一渲染行(前端 UI 内部结构,非后端契约) */
export interface FileRow {
  kind: 'file' | 'folder'
  id: number
  name: string
  size: number
  type: string
  updatedAt: string
  uploadedBy: number
  /** 可见组 ID 数组(空 = 不限制,按桶权限) */
  visibleGroups: VisibleToGroups
}

/** 从 ListResult 组装统一渲染行(文件夹在前,文件在后,按 created_at 混排与后端一致) */
export function toRows(res: ListResult): FileRow[] {
  const rows: FileRow[] = []
  res.folders.forEach((d) =>
    rows.push({
      kind: 'folder',
      id: d.ID,
      name: d.Name,
      size: 0,
      type: 'Folder',
      updatedAt: d.UpdatedAt,
      uploadedBy: d.UploadedBy,
      visibleGroups: parseVisibleGroups(d.VisibleToGroups),
    }),
  )
  res.files.forEach((f) =>
    rows.push({
      kind: 'file',
      id: f.ID,
      name: f.Name,
      size: f.FileSize,
      type: f.FileType,
      updatedAt: f.UpdatedAt,
      uploadedBy: f.UploadedBy,
      visibleGroups: parseVisibleGroups(f.VisibleToGroups),
    }),
  )
  return rows
}

/** 分享链接(后端返回时 Password 已脱敏) */
export interface ShareLink {
  ID: number
  CreatedAt: string
  UpdatedAt: string
  DeletedAt: string | null
  BucketItemID: number
  CreatorID: number
  /** 分享短码(URL 用) */
  Token: string
  /** read | edit */
  Permission: string
  /** 过期时间(null = 永久) */
  ExpiresAt: string | null
  /** 下载次数上限(0 = 不限) */
  MaxDownloads: number
  /** 已下载次数 */
  DownloadCount: number
}

/** 分页结果(后端统一 {total, items} 结构) */
export interface PageResult<T> {
  total: number
  items: T[]
}

/** 批量上传结果(后端逐文件独立处理,部分失败不中断) */
export interface BatchUploadResult {
  success: FileItem[]
  failed: { name: string; error: string }[]
}

// ---- 批量操作类型(2026-08-07,与 server/batch.go 契约对应) ----

/** 批量操作条目引用(按 Kind 分派文件/文件夹) */
export interface BatchItem {
  kind: 'file' | 'folder'
  /** 源条目 ID(files.id / folders.id) */
  id: number
}

/** 单条复制指令(行式切片;指针字段缺省 = 继承批量级) */
export interface CopyItem extends BatchItem {
  /** 源桶;必传(调用方填入当前桶;后端无缺省回退) */
  src_bucket_id: number
  /** 目标桶;缺省 = 批量级 dst_bucket_id */
  dst_bucket_id?: number
  /** 目标目录;缺省 = 批量级 dst_dir */
  dst_dir?: string
  /** 目标名;缺省 = 沿用源名 */
  dst_name?: string
}

/** 单条移动指令(同 CopyItem;缺省目标桶 = 同桶) */
export interface MoveItem extends BatchItem {
  src_bucket_id: number
  dst_bucket_id?: number
  dst_dir?: string
  dst_name?: string
}

/** 单条下载指令 */
export interface DownloadItem extends BatchItem {}

/** 批量操作单条结果 */
export interface BatchResultItem {
  kind: 'file' | 'folder'
  id: number
  name?: string
  /** 空 = 成功;非空 = 失败原因 */
  error?: string
}

/** 批量操作结果(success/failed 分离,HTTP 恒 200) */
export interface BatchResult {
  success: BatchResultItem[]
  failed: BatchResultItem[]
}

/** 是否为文件夹行(渲染行) */
export function isFolder(row: FileRow): boolean {
  return row.kind === 'folder'
}

/** 字节数 → 可读大小 */
export function formatSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let n = bytes
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${units[i]}`
}

/** RFC3339 时间字符串 → 本地可读时间 */
export function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

/** 权限级别 → 中文名(与后端 model.PermissionLevel.String() 对应) */
export function permissionLabel(level: number): string {
  const map: Record<number, string> = {
    0: '超级管理员',
    1: '管理员',
    2: '特殊用户',
    3: '普通用户',
  }
  return map[level] ?? `级别 ${level}`
}
