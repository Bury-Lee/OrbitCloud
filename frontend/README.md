# OrbitCloud · 前端工程(Vue3)

> 技术栈:Vue 3.5 + Vite 6 + TypeScript + Element Plus 2 + Pinia + Vue Router 4 + Axios
> 后端:orbitcloud(Go/Gin,见仓库根 README)

---

## 1. 环境要求

| 项 | 版本 | 说明 |
|---|---|---|
| Node.js | ≥ 18 | `node --version` 确认 |
| npm | ≥ 9 | Windows PowerShell 下若拦截 `npm.ps1`,用 `npm.cmd` |

## 2. 安装与启动

```bash
cd frontend

npm.cmd install        # 首次安装依赖(Windows PowerShell 下用 npm.cmd;普通 shell 用 npm)
npm.cmd run dev        # 开发模式(热更新,默认 http://localhost:5173)
npm.cmd run build      # 生产构建(输出 dist/,含 vue-tsc 类型检查)
npm.cmd run preview    # 预览构建产物
npm.cmd run typecheck  # 仅类型检查
```

## 3. 与后端对接

### 3.1 后端启动

```bash
cd <仓库根>
go build -o orbitcloud .
./orbitcloud -initConfig          # 首次:生成 config.yaml
./orbitcloud --add-superadmin admin admin123456   # 创建超级管理员(仅一次)
./orbitcloud                      # 默认监听 0.0.0.0:8080
```

### 3.2 开发代理(已配置,无需 CORS)

`vite.config.ts` 已配置代理:`/api` → `http://127.0.0.1:8080`。前端请求一律走相对路径
`/api/v1/...`,由 Vite 转发到后端;同源代理使开发期无需后端开放 CORS。

后端响应格式:成功 `{code:0, data:{...}, message:"ok"}`;错误 `{code:-1, message:"..."}`。

### 3.3 生产部署

`npm run build` 产物 `dist/`,由 Nginx 托管并反代 `/api` 到后端:

```nginx
server {
    listen 80;
    root /var/www/orbitcloud/dist;
    location / { try_files $uri $uri/ /index.html; }        # SPA history 路由回退
    location /api/ { proxy_pass http://127.0.0.1:8080; }    # API 反代
}
```

## 4. 目录结构

```
frontend/
├── index.html                 # 入口 HTML
├── vite.config.ts             # Vite 配置(代理/别名/构建)
├── tsconfig.json              # TS 配置(@/* → src/*)
├── public/favicon.svg
└── src/
    ├── main.ts                # 应用入口(挂载 Element Plus/Pinia/Router;注册会话过期回调)
    ├── App.vue                # 根组件(仅路由出口)
    ├── styles/main.css        # 全局样式
    ├── api/
    │   ├── http.ts            # Axios 实例:baseURL=/api/v1;JWT 注入;统一解包;401 刷新重放
    │   ├── types.ts           # 后端实体类型与工具函数
    │   ├── auth.ts / buckets.ts / files.ts / groups.ts / shares.ts / users.ts  # 各模块 API
    ├── stores/auth.ts         # Pinia 认证状态:令牌持久化/登录登出/用户信息
    ├── composables/           # useDialog(弹窗互斥)/ useListState(列表三态)/
    │                          # useStructuredUpload/useStructuredDownload/useFolderDownload
    ├── components/            # AppLayout(布局与导航)/ FolderTreePicker(目录选择)
    ├── router/index.ts        # 路由与守卫(requiresAuth + requiresAdmin)
    └── views/                 # Login/Forbidden/Buckets/Files/Shares/MyGroups/Groups/Users
```

## 5. 认证与令牌

- 登录返回 `{access_token, refresh_token, user}`;access_token 存 localStorage,
  `http.ts` 请求拦截器自动加 `Authorization: Bearer <token>`;
- 刷新令牌轮换:响应 401 时先 `POST /auth/refresh` 换新令牌对再重放原请求
  (并发 401 共享同一刷新 Promise,避免重复刷新);刷新失败 → 清凭证跳登录页;
- 登录时保存 `user`(含 `PermissionLevel`),用于判断管理员身份(`permission_level <= 1`);
- 路由守卫:未登录访问受保护页 → `/login?redirect=...`;非管理员访问 `/admin/*`
  → `/403`。

## 6. 页面

| 路由 | 页面 | 功能 |
|---|---|---|
| `/` | 桶列表 | 新建/编辑/删除/进入 |
| `/buckets/:id` | 文件管理 | 面包屑目录导航(URL query 承载,刷新不丢位置) · 拖拽批量上传 · 新建文件夹 · 下载 · 在线预览 · 重命名 · 移动/复制 · 分享 · 可见组设置 · 删除 |
| `/shares` | 分享管理 | 列表 · 复制链接 · 删除 |
| `/groups` | 我的组 | 我加入的用户组列表 |
| `/admin/users` | 用户管理 | 用户列表 · 新建 · 编辑(权限/状态/密码) · 删除 |
| `/admin/groups` | 组管理 | 组 CRUD · 成员管理 |
| `/403` | 无权限 | 非管理员访问管理路由的落点 |
| `/login` | 登录 | `POST /auth/login` |

> 下载/预览走独立 axios 实例(blob),避免统一响应拦截器误拆二进制流。

## 7. 常见问题

| 问题 | 原因/解决 |
|---|---|
| `npm` 报 "禁止运行脚本" | PowerShell 策略:用 `npm.cmd`,或 `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned` |
| 登录提示网络错误/500 | 后端未启动;先按 §3.1 启动 orbitcloud |
| 登录 401 | 账号不存在或密码 <8 位;admin 需后端 `--add-superadmin` 创建 |
| 登录成功但跳转后又回登录页 | access_token 过期且刷新失败;重新登录 |
| 端口 5173 被占 | `vite.config.ts` 改 `server.port` |
