# OrbitCloud API 参考

> 基础:`BaseURL = http(s)://<host>/api/v1`,除标注「公开」外全部需要
> `Authorization: Bearer <access_token>`。
>
> 响应格式:
>
> - 成功:`HTTP 200`,`{"code": 0, "data": {...}, "message": "ok"}`
> - 失败:`HTTP 4xx/5xx`,`{"code": -1, "message": "..."}`(`500` 一律为 `"internal error"`,不暴露细节)

## 1. 错误码(HTTP 状态码语义)

| 状态码 | 哨兵错误 | 典型场景 |
|---|---|---|
| 400 | invalid input | 参数缺失/非法、JSON 解析失败、文件为空 |
| 401 | unauthorized | 未登录、令牌过期/无效、刷新令牌已吊销 |
| 403 | forbidden | 权限不足、账号禁用、非资源属主 |
| 404 | not found | 桶/文件/文件夹/分享不存在 |
| 409 | conflict | 用户名/桶名重复、重复创建 |
| 413 | quota exceeded | 桶容量配额不足 |
| 416 | range not satisfiable | Range 头非法/区间越界(响应带 `Content-Range: bytes */size`) |
| 500 | internal error | 未知错误(对外统一文案) |

## 2. 认证与用户

### POST `/auth/login`(公开)

请求:`{"username": "admin", "password": "admin123456"}`

```json
{
  "code": 0,
  "data": {
    "access_token": "eyJ...",
    "expires_in": 43200,
    "refresh_token": "eyJ...",
    "user": {
      "id": 1, "username": "admin", "name": "admin",
      "email": "", "permission_level": 0, "status": 1,
      "created_at": "2026-08-01T10:00:00+08:00"
    }
  },
  "message": "ok"
}
```

### POST `/auth/refresh`(公开)

请求:`{"refresh_token": "..."}` → 返回新令牌对(轮换:旧刷新令牌即刻失效)+ user。

### POST `/auth/logout`(登录)

请求:`{"refresh_token": "..."}` → `204`,幂等(令牌已吊销同样成功)。

### POST `/auth/register`(管理员,权限 ≤ 1)

请求:`{"username": "alice", "password": "alice123456", "permission_level": 5}`
→ 返回创建的用户。**权限 0 不可经 API 创建**(一律归一为普通用户),仅 `--add-superadmin` 可建。

### GET `/users/me` / PUT `/users/me`(登录)

- GET → 当前用户信息;
- PUT 请求:`{"name"?, "email"?, "old_password"?, "new_password"?}` → 更新本人资料/密码。

### GET `/users?page=&page_size=`(管理员)

→ `{"total": n, "items": [user...]}`。

### PUT `/users/:id` / DELETE `/users/:id`(管理员)

- PUT:`{"permission_level"?, "status"?, "password"?, "name"?}`(操作者权限须严格高于目标);
- DELETE:软删。

## 3. 用户组

| 方法/路径 | 鉴权 | 说明 |
|---|---|---|
| GET `/users/me/groups` | 登录 | 我加入的组 |
| POST `/groups` | 管理员 | 创建:`{"name", "description"?}` |
| GET `/groups`、`/groups/:id` | 管理员 | 列表 / 详情 |
| PUT/DELETE `/groups/:id` | 管理员 | 修改 / 删除 |
| POST `/groups/:id/members` | 管理员 | 添加成员:`{"user_ids": [1,2]}` |
| DELETE `/groups/:id/members/:uid` | 管理员 | 移除成员 |

## 4. 桶

| 方法/路径 | 说明 |
|---|---|
| POST `/buckets` | 创建:`{"name", "description"?, "quota"?}`(quota 字节,0=不限);桶名全局唯一 |
| GET `/buckets` | 我的桶列表 |
| GET `/buckets/:id` | 详情(含 `quota` / `used_space`) |
| PUT `/buckets/:id` | 修改(`name? / description? / quota?`) |
| DELETE `/buckets/:id` | 删除:桶置禁用 → 落 DeleteTask → 后台级联清理后删记录 |

## 5. 文件与文件夹

> 按类型分离寻址:**文件** `/files/:fid`、**文件夹** `/dirs/:fid`;
> 对象存储中的 key = 文件记录主键 ID,桶名 = 桶 ID 的 36 进制编码,不依赖路径/文件名。

### 上传

| 方法/路径 | 说明 |
|---|---|
| POST `/buckets/:id/files?path=dir/sub` | multipart 单文件(字段 `file`);`path` 为目标目录(缺省桶根 `/`),父链自动创建;返回 `model.File` |
| POST `/buckets/:id/files/batch?path=&folder_id=` | multipart 多文件(字段 `files`);逐文件独立处理,部分失败不影响其它,返回 `{success: [...], failed: [{name, reason}...]}` |

### 元数据与列表

| 方法/路径 | 说明 |
|---|---|
| GET `/buckets/:id/files?path=&page=&page_size=` | 目录列表(分页),`path` 缺省桶根 |
| GET `/buckets/:id/files/:fid` | 文件元数据 |
| GET `/buckets/:id/dirs/:fid` | 文件夹元数据(含 `isable`,删除中为 false) |

### 下载与预览(流式)

| 方法/路径 | 说明 |
|---|---|
| GET `/buckets/:id/files/:fid/download` | 流式下载;支持 `Range` 断点续传(416 处理见错误码表) |
| GET `/buckets/:id/files/:fid/preview` | 在线预览(浏览器内打开/内联播放) |
| GET `/buckets/:id/files/:fid/stream` | 流式访问(**QueryToken 鉴权**,给播放器/内嵌页临时 token 用) |

### 目录操作

| 方法/路径 | 说明 |
|---|---|
| POST `/buckets/:id/dirs` | 新建文件夹:`{"name", "path"?}`(mkdir -p 建父链) |
| POST `/buckets/:id/dirs/:fid/move` | 移动/重命名文件夹 |
| DELETE `/buckets/:id/dirs/:fid` | 删除文件夹:置 `isable=false` → 落 DeleteTask → 后台深度优先硬删(崩溃可续跑) |
| POST `/buckets/:id/dirs/:fid/copy` | 复制文件夹(落 CopyTask 后台处理) |

### 文件操作

| 方法/路径 | 说明 |
|---|---|
| POST `/buckets/:id/files/:fid/copy` | 复制:`{"target_folder_id"?}` |
| POST `/buckets/:id/files/:fid/move` | 移动/重命名:`{"name"?, "target_folder_id"?}` |
| DELETE `/buckets/:id/files/:fid` | 删除文件(立即删对象 + 记录) |

### 批量操作(≤ 100 条,逐条独立)

| 方法/路径 | 说明 |
|---|---|
| POST `/buckets/:id/items/batch-delete` | `{"items": [{type: "file"\|"folder", id}]}` |
| POST `/buckets/:id/items/batch-copy` | 同上 + `target_folder_id` |
| POST `/buckets/:id/items/batch-move` | 同上 |
| GET `/buckets/:id/items/batch-download` | 批量下载(zip),失败项逐条返回 |

### 可见性(条目级 ACL)

| 方法/路径 | 说明 |
|---|---|
| PUT `/buckets/:id/files/:fid/visibility` | `{"group_ids": [1,2]}`(空数组 = 不限制) |
| PUT `/buckets/:id/dirs/:fid/visibility` | 同上(文件夹) |

## 6. 分享

| 方法/路径 | 鉴权 | 说明 |
|---|---|---|
| POST `/shares` | 登录(属主/管理员) | 创建:`{"file_id", "permission"?("read"), "expires_at"?, "max_downloads"?, "password"?}`;`file_id` 可为文件或文件夹 ID |
| GET `/shares?page=&page_size=` | 登录 | 我创建的分享(分页) |
| PUT `/shares/:id` | 创建者 | 更新:`{"expires_at"?, "max_downloads"?, "password"?}` |
| DELETE `/shares/:id` | 创建者 | 删除 |
| GET `/share/:token` | 公开 | 解析分享元数据;有提取码带 `?password=`;**下载/预览才计数** |

## 7. 鉴权与中间件要点

- 登录后 access_token 放入 `Authorization: Bearer <token>`;刷新令牌轮换:收到 401 先
  `POST /auth/refresh` 换新令牌对并重放原请求(前端已实现并发共享刷新);
- 管理员判定:`permission_level <= 1`;管理员改他人须权限严格高于目标;
- 条目可见性:条目挂 `visible_to_groups`(JSON 组 ID 列表),空 = 不限制;组内成员可见,
  非成员与管理员不受限。
