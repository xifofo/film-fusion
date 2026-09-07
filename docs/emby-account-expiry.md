# Emby 账户到期管理

入口：Emby → 账户管理（`/emby/accounts`）。系统设置中需配置 Emby 地址和具有管理权限的 API Key；本功能不依赖 Emby 代理开关。

- 页面实时读取 Emby 用户、禁用状态、管理员标记和最近登录时间，支持搜索、状态筛选及到期时间排序。
- 设置精确到秒的到期时间。浏览器按本地时区显示，API 使用带时区的 RFC3339 时间，数据库统一存 UTC。
- 清空日期并保存表示永不过期。修改或清空日期不会自动启用已禁用的账户。
- FilmFusion 后台启动时立即检查，运行期间每 30 秒检查一次，保存后也会唤醒检查。服务需保持运行；停机期间到期的账户会在下次启动后处理。
- 禁用前读取完整的最新 UserPolicy，只修改 `IsDisabled`，提交后重新读取确认。请求失败或未确认禁用会记录错误，并在后续检查中重试；一个账户失败不会阻断其他账户。
- 已到期账户如果在 Emby 中被重新启用，会在下一次检查中再次禁用。恢复使用前应先延长或清空到期时间。
- 到期记录以 Emby 的 ServerId 和用户 Id 联合隔离；切换服务器后旧设置不会用于新服务器。列表不返回用户完整权限或 API Key。

接口均位于现有 JWT 与 FilmFusion 管理员认证组内：

- `GET /api/emby-accounts`：返回用户列表及本地到期设置、执行时间、最近错误。
- `PUT /api/emby-accounts/:user_id/expiry`：请求体 `{"server_id":"...","expires_at":"2030-01-01T08:00:00+08:00"}`；取消使用显式 `null`，省略字段会返回 400。设置已过去的时间会立即进入后台禁用流程。

数据库启动迁移自动创建 `emby_account_expiries`。无需配置外部 cron。调度与保存通过当前 FilmFusion 进程内的互斥锁协调，按项目现有单后端实例方式运行。

Emby 协议参考：[更新用户策略](https://dev.emby.media/reference/RestAPI/UserService/postUsersByIdPolicy.html)。
