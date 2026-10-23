# PlutoFish 转存结果通知

FilmFusion 后端主动用 HTTP 补收 + SSE 订阅通知，不需要 NAS 公网入口、Webhook 或端口映射。也支持显式 HTTP 轮询。通知与网盘发现请求仅访问配置服务的 `/api/v1/events`、`/api/v1/events/stream` 和 `/api/v1/events/accounts`，使用独立的 `events:read` Bearer API Key。

## 配置与运行

1. 在 PlutoFish「我的 → 通知接入」创建 API Key。在 FilmFusion 管理页 **PlutoFish 通知** 本地填写；不要把真实密钥写入示例或提交到 Git。
2. 服务地址默认 `https://plutofish.shop`，填写服务根地址，不要粘贴完整 `/api/v1/events` 地址。生产只接受 HTTPS，HTTP 仅允许 loopback 模拟服务器。
3. 点击 **获取接收网盘**：后端使用 API Key 获取本人网盘列表，并自动识别所属平台 UID，无需手填用户或网盘 ID。已保存配置会自动读取列表；更新密钥或服务地址后需重新获取。更换服务或用户会清空当前表单的网盘映射，旧服务/用户的消费进度仍保留；相同 UID 换钥匙沿用原游标。
4. 默认 SSE；不适合长连接的环境选 HTTP 轮询，间隔至少 10 秒并有随机抖动。启用并保存后立即开始消费；服务启动时恢复已启用的配置。**停止**取消当前运行；要跨重启停用，请关闭启用开关并保存。
5. 可选：在 **PlutoFish 接收网盘** 下拉框按名称选择，再选择对应的本地「云路径映射」。客户端自动保存 `targetAccountId`，不需要从通知中复制。停用网盘不能新选；原映射的网盘被移除时会提示重新选择。云路径仍需对应同一个实际接收账号与路径，不能仅靠同名猜测。

配置复用现有 `system_configs` SQLite 运行配置机制，私有键为 `plutofish.settings`。API Key 仅写入本机数据库，GET 配置只返回 `keyConfigured`，空密钥表示保留原值；不保存到浏览器 localStorage。日志不含请求头、密钥或远端错误响应正文。请像现有网盘/Emby 凭据一样限制 `data/film-fusion.db` 及备份的读取权限。该版本没有额外的静态加密密钥服务。

配置请求示例（占位符不是有效密钥；实际请在页面输入）：

```json
{
  "enabled": false,
  "baseURL": "https://plutofish.shop",
  "ownerUID": "由网盘列表接口自动返回",
  "mode": "sse",
  "pollSeconds": 15,
  "apiKey": "仅在本机填写",
  "mappings": [{"targetAccountID": "下拉选择自动保存的网盘ID", "cloudPathID": 1}]
}
```

后端照常运行 `go run . server`，前端开发使用 `pnpm dev`；前端 Vite 默认代理后端 9000 端口，如果后端使用 5000 等其他端口，启动时设置 `VITE_API_TARGET=http://127.0.0.1:5000 pnpm dev`。生产按项目现有构建部署方式更新后端和前端。不要为了测试本功能启动已有的 115 自动化/保活任务：下面的专用模拟测试和诊断程序无需运行整个 FilmFusion。

## 本地成功处理范围

成功事件可靠入队后，`PlutoFishBusiness.Handle` 接入项目现有的 CloudPath STRM 内容前缀、路径与编码规则，生成**本地 STRM**。它不调用通用 STRM 入口中的网盘查询/下载分支，不扫描或操作 115，也不虚构播放接口。输出位于对应映射的 `LocalPath/PlutoFish/<服务用户作用域哈希>/<转存任务哈希>/` 下，保留源文件名称并改成 `.strm`。

需要完整的真实 `targetDirectoryPath` 和 `fileName`、匹配的账号映射、有效媒体过滤与内容前缀。空路径不会通过 pickcode 或文件 ID 猜测。即使状态是 succeeded，资料不够也标为「待人工核对」，显示原因；补好映射后可点「重新处理本地 STRM」。缺失的上游文件定位需要在 PlutoFish 核对或另行扩展受授权的定位回调。此按钮不重试网盘转存。

failed 仅记录失败；unknown 保留待核对；未知 schema/type 保存完整原始 payload 到本地队列，显示不支持原因。均不执行成功逻辑。页面分别显示上游转存状态和本地处理状态，不能将已接收通知等同于可播放或 Emby 已入库。

**尚未自动接入 Emby 刷新**：通知消费与 STRM 创建无需 Emby。现有内容前缀指向的 OpenList/CloudDrive/播放解析服务及媒体服务器仍需正确配置并可达，实际播放可能请求网盘，这与接收通知是两个授权范围。媒体库可使用已有扫描机制；如需通知后自动刷新，应另行配置并授权真实 Emby 服务，再在本地成功处理回调后接入可幂等的刷新任务。

## 持久化与恢复

- `pluto_fish_cursors` 按规范化服务地址 + 显式平台 UID 的哈希隔离，游标不拆分、不比较序号。
- `pluto_fish_events` 是持久化去重账本兼待处理队列。HTTP 一页的所有事件与 nextCursor 在同一事务提交，包括 items 为空的扫描进度。SSE 事件先持久化，只有后续 cursor 帧提交进度；失败会断流并从已提交位置补收。
- 收件串行处理，解析器仅持有一个有大小上限的帧，TCP 背压避免无界内存队列。pending/review 合计最多 10000；满时拒绝整页事务、保留游标并退避重连，清理积压后恢复。历史去重账本不自动删除。
- `pluto_fish_receipts` 仅在本地成功后按 scope + transferId 记录，failed/unknown 不占成功去重键。STRM 路径确定、原子发布，相同内容可重试；已有不同内容不会覆盖。任务目录里的 `.transfer.json` 固定本次文件名和内容，避免崩溃恢复时改变名称再生成第二份。若写文件后数据库提交失败，重启后重做幂等回调。本地文件系统需支持硬链接和 fsync；不支持的 NAS 挂载会保留待处理并报错，不会误报完成。
- `pluto_fish_business_plans` 在第一次写文件前固定输出根目录、相对路径和内容，防止写入后未记账时更改映射并产生第二份文件。存在计划后改变目录/内容会进入待核对；恢复原映射可继续。没有完整定位信息的事件不会提前占用计划。
- 401、422、403 和其他配置/协议错误会停止自动重试并显示原因；网络/可恢复服务错误指数退避，常规最少 5 秒、上限 120 秒并抖动，429/503 的 Retry-After 可以延长等待。SSE 无短总超时，网络读空闲 60 秒时断开。
- 每个 FilmFusion 进程单配置只有一个消费循环和一条 SSE；不要用多个进程同时共享这份配置/SQLite 数据目录。配置保存会先取消并等待旧循环退出，再启动新循环。

## 模拟测试与最小联调

以下均从 `film-fusion/` 目录执行：

```sh
go test ./app/plutofish
go test ./app/service ./app/handler -run PlutoFish
go test -race ./app/plutofish
go test -race ./app/service ./app/handler -run PlutoFish
go run ./tools/plutofish-mock
```

模拟服务只监听 `127.0.0.1:18765`。页面填服务地址 `http://127.0.0.1:18765`、假密钥 `local-mock-key`，点击获取接收网盘即可看到「模拟接收网盘」，所属 UID 自动识别为 `mock-user`。它会发送多页、空页进度、先失败后成功、缺定位的成功、unknown、SSE 分块重复投递与心跳；成功事件应留待处理，不生成假 STRM。

专用限时诊断仅读取通知，不启动 FilmFusion 的其他后台业务，不生成 STRM、不操作网盘。诊断进度只在内存，不作为生产消费进度；输出仅聚合计数和脱敏连接状态：

```sh
printf '%s' 'local-mock-key' | go run ./tools/plutofish-probe -base-url http://127.0.0.1:18765 -duration 20s
```

真实接口只在用户明确授权后测试。使用交互输入，密钥不出现在命令参数或 shell 历史里（zsh）：

```sh
read -rs 'pf_test_key?PlutoFish API Key: '
printf '%s' "$pf_test_key" | go run ./tools/plutofish-probe -base-url https://plutofish.shop -duration 20s
unset pf_test_key
```

网盘发现使用 `POST /api/plutofish/accounts/query`，请求体为 `{ "baseURL": "服务根地址", "apiKey": "新密钥或省略以使用已保存密钥" }`；返回 `ownerUID` 和安全网盘摘要。请求不保存密钥、不启动消费，只查 PlutoFish 本地记录；不读取 115。已保存密钥只用于原服务地址和原本地管理员，禁止跟随重定向；失败不返回部分列表。PlutoFish 返回 404 时明确提示升级服务端。

管理 API 均在已有后台管理员认证内，路径 `/api/plutofish/{config,status,events,start,stop}` 与 `/api/plutofish/events/:id/retry`。配置和状态 GET；配置 PUT；启动、停止、重处理 POST。该配置模块不授予已有外部 API Token scope，使用后台登录 JWT。

```sh
curl -fsS -H "Authorization: Bearer $FILMFUSION_JWT" http://127.0.0.1:5000/api/plutofish/status
```

模拟覆盖帧分块/心跳、分页空页、重复、失败不跳游标、数据库重开恢复、failed 后 succeeded、缺定位、未知版本、401/422、429/503 和主动关闭。模拟成功仅证明代码在本地模拟边界内的行为；真实 NAS 连接、接收真实转存事件、已有播放前缀、网盘播放和 Emby 入库需要分别验证。

2026-10-22 经用户提供测试密钥并授权后，专用诊断程序对真实服务完成一次 20 秒只读测试：HTTP 补收成功，SSE 建连成功，超时主动关闭，返回 0 条历史事件。密钥未写入项目或持久化配置。此结果验证了当时的鉴权与连接，不证明真实转存事件投递、NAS、网盘播放或 Emby 入库成功。
