# API Token 接入

管理员在控制台 **API Token** 页面创建凭据，选择授权模块和有效期。
完整凭据以 `ffapi_` 开头，只在创建成功时返回一次；数据库只保存 SHA-256 摘要及展示前缀。
每个应用应独立使用一个 Token，遗失或轮换时创建新 Token，再撤销旧 Token。

## 调用业务 API

继续使用现有 `/api/...` 路径和请求/响应格式，在请求头携带：

```http
Authorization: Bearer ffapi_<your-token>
```

例如，勾选「Emby 媒体统计」后：

```sh
curl 'https://filmfusion.example.com/api/emby-stats' \
  -H "Authorization: Bearer ${FILMFUSION_API_TOKEN}"
```

Token 仅接受 Authorization 请求头，不支持 URL 查询参数或 Cookie。
跨机器调用使用 HTTPS，Token 应保存在接入应用的服务端。
原有后台登录 JWT 及其图片、下载查询参数用法保持兼容。

## 授权边界

- 创建时必须选择至少一个已知模块，不支持通配符或自动授予新增模块。
- 模块授权覆盖该模块的查询和修改操作，不是只读权限。
- 「云存储管理」包含账号凭据、115 授权和目录访问；「下载器设置」和「自动化中心」包含下载器连接配置。按应用需要授权。
- 每次调用都会检查 Token 到期、撤销和所属管理员的启用状态。管理员停用、删除或失去管理员身份后，其 Token 不再有效。
- `/api/api-tokens`、`/api/me`、`/api/app-config`、`/api/config` 和 `/api/site-assets` 等没有开放模块的接口仅接受后台登录 JWT。
- 现有登录、刷新、公开资源和 Webhook 保持各自原来的鉴权方式。API Token 不能换取登录 JWT。
- 路由模块映射由服务端 `app/auth/api_token_scopes.go` 维护，以匹配后的路由和路径段边界判断，未登记的模块默认拒绝。

失败返回 HTTP 401（无效、过期、已撤销或所属管理员不可用）、403（未授权模块）或 503（鉴权存储不可用）。
撤销作用于后续请求，不会取消已经通过鉴权的任务或持续连接。
「最近使用」记录通过鉴权及模块授权的调用，按分钟合并写入，不是逐请求审计日志。

## Token 管理 API（仅后台登录 JWT）

| 方法 | 路径 | 功能 |
| --- | --- | --- |
| GET | `/api/api-tokens` | 当前管理员颁发的 Token 元数据列表 |
| GET | `/api/api-tokens/scopes` | 可授权模块、说明及路由前缀 |
| POST | `/api/api-tokens` | 创建 Token |
| POST | `/api/api-tokens/:id/revoke` | 撤销当前管理员颁发的 Token，重复调用幂等 |

创建请求：

```json
{
  "name": "客厅 Emby 插件",
  "scopes": ["emby-stats"],
  "expires_at": "2027-01-01T00:00:00Z"
}
```

`expires_at` 为 RFC3339 时间；`null` 或省略表示永不过期。
成功返回 HTTP 201 和 `{ "code": 0, "data": { "token": "ffapi_...", "record": { ... } } }`。
后续列表不会返回完整 Token 或摘要。创建和列表响应设置 `Cache-Control: no-store`。

此功能提供通用 API 凭据；Emby 原生插件及专用直链解析 API 需分别实现。
