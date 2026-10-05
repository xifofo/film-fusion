# 后端运行与性能维护

业务 API、识别引擎选择、命名与洗版规则、任务间隔和重试次数保持原有约定。

## 整理模块

`app/organize/Service` 是 HTTP、自动化、预整理队列共用的业务服务。计划生成、确认执行、洗版恢复、目录解析、版本评分和预整理分别维护；`http*.go` 负责 Gin 入口，`app/handler/organize.go` 保留原有类型与构造入口兼容。

每个请求或任务使用独立的 context 作用域，共享底层连接池、缓存和 MoviePilot 令牌状态。预览和外部读取使用调用方的取消信号；已开始执行的计划状态、租约释放和恢复记录仍完成持久化，避免取消请求破坏恢复信息。

115 Open SDK 调用使用独立的账号令牌副本，复用 HTTP 连接。整理、下载队列、STRM 遍历和播放代理不会在并发调用中修改同一个 SDK 的账号令牌。`sdk115helper.WithAccessToken` 按当前固定的 SDK v0.2.2 字段布局实现；升级 SDK 时需重新核对可复制性并运行多账号并发回归。

## 代码布局

同一模块按职责分文件，继续使用原有 package、类型和构造入口。服务入口集中维护依赖与生命周期，具体操作与对应的数据类型放在一起。

| 模块 | 入口与职责位置 |
| --- | --- |
| 整理 | `app/organize/organize.go` 串联整理流程；`recognition.go` 处理识别输入，`shadow.go` 处理影子对比，`paths.go` 生成路径，`library.go` 查询媒体库，`subtitles.go` 匹配和下载字幕，`files.go` 执行文件操作；版本规则集中在 `versions.go` |
| MoviePilot | `moviepilot_service.go` 负责依赖与启停；`moviepilot_token.go` 维护共享认证，`moviepilot_http.go` 处理 HTTP，`moviepilot_recognition.go`、`moviepilot_categories.go`、`moviepilot_transfer.go` 分别负责识别、分类和重命名；响应解析与本地回退分别位于 `moviepilot_parser.go`、`moviepilot_fallback.go` |
| 播放负载均衡 | `match302_balance_service.go` 负责服务入口与源文件信息；`match302_balance_playback.go` 决定播放策略，`match302_balance_assignments.go` 管理分配状态，`match302_balance_transfer.go` 执行转存，`match302_balance_directories.go` 处理云盘目录，`match302_balance_range_reader.go` 实现 HTTP 分段读取 |
| 自动化服务 | `automation_service.go` 负责服务启停；`automation_workflows.go` 管理流程配置，`automation_targets.go` 管理执行目标，`automation_source_scheduler.go` 调度扫描，`automation_runs.go` 管理运行、重试和取消 |
| 自动化引擎 | `automation_engine.go` 推进运行状态；`automation_graph.go` 处理图依赖与运行上下文，`automation_node_dispatch.go` 分派节点，`automation_conditions.go` 处理条件与比较，`automation_text_nodes.go` 执行文本节点；日期、列表、分支、取首个有效值和遍历操作各自维护校验与执行代码 |
| 自动化定义 | `automation_definition.go` 定义协议与序列化；`automation_definition_validation.go` 校验图结构，`automation_node_validation.go` 分派节点配置校验，`automation_source_validation.go` 校验触发源，`automation_config_values.go` 读取配置字段 |
| Emby 缺集 | `emby_missing_service.go` 维护依赖、设置和查询；`emby_missing_scan.go` 执行扫描，`emby_missing_tasks.go` 管理任务与进度，`emby_missing_resolve.go` 解析媒体路径，`emby_missing_blacklist.go` 处理黑名单，`emby_missing_schedule.go` 管理定时任务 |
| Emby 代理 | `app/handler/emby_proxy.go` 保留反向代理入口；`emby_proxy_accounts.go` 解析账号，`emby_proxy_download.go` 获取直链，`emby_proxy_balance.go` 处理播放分配，`emby_proxy_playback.go` 处理播放事件与预热，`emby_proxy_logging.go` 记录日志 |
| HTTP 服务 | `app/server/server.go` 负责构造、启动和停机；`app/server/routes.go` 注册路由、中间件与服务关联 |

除表中明确标注的目录外，服务文件均位于 `app/service/`。增加功能时沿对应职责维护，类型专用的校验和辅助函数与实现放在一起；跨职责共享代码再提取为模块内文件。

## 缓存与认证请求

- 115 目录缓存每个实例最多保留 512 个完整父目录列表、8 MiB 估算数据，估算包含字符串和结构开销，不等于进程 RSS 上限。整理仍使用原有的 10 分钟 TTL；新增子目录不会延长 TTL。
- 缓存按最近访问顺序淘汰，操作时定期回收过期项。超大目录跳过缓存，继续原有回源查询；不会返回被截断的目录列表。账号隔离、空目录缓存、返回副本语义保持不变。
- 共享令牌状态的 MoviePilot 调用合并同时进行的登录。等待者可以独立取消；首个调用被取消后，仍存活的调用者可重新获取令牌，认证失败不会被永久缓存。
- MoviePilot GET 遇到 401 时最多重新认证并重发一次，与原有手动整理接口一致；持续 401 返回认证错误，避免递归重试。业务任务的重试次数和识别回退规则不变。

## SQLite

数据库路径仍为 `data/film-fusion.db`。进程内使用一个连接，串行执行 SQLite 写事务，事务内的查询必须使用收到的 `tx`。锁等待默认 5 秒。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `FILMFUSION_SQLITE_BUSY_TIMEOUT_MS` | `5000` | 外部连接持锁时的等待上限，范围 1–60000 毫秒 |
| `FILMFUSION_SQLITE_JOURNAL_MODE` | 不设置 | 保留数据库原有模式；可显式设为 `WAL` 或 `DELETE` |

本地磁盘部署可设置 `FILMFUSION_SQLITE_JOURNAL_MODE=WAL`。网络文件系统继续使用兼容的日志模式。显式选择日志模式时仍使用 `synchronous=FULL`，保留提交持久性。启动日志会输出实际模式与连接设置。配置只在下次启动生效。

SQL 超过 500 毫秒或执行出错时，日志记录操作类型、耗时、行数、应用调用位置，以及连接池累计等待次数和时间；不输出 SQL 文本与绑定参数。媒体任务的排队耗时记录在 debug 日志中。

## 请求、任务与停机

- Emby 元数据请求和整理使用的 115 Web 请求有 30 秒请求上限；MoviePilot 原有请求上限保持不变。洗版字幕仍限制为 1 分钟。
- 整理请求和自动化节点的取消信号传递到 Emby、MoviePilot、TMDB、115 调用及主要数据库读取。
- Emby 用户解析和媒体库统计同样传递请求取消信号；统计继续保留成功结果及局部错误。
- 停机先通知 SSE 连接结束，并排空管理端、Emby 代理 HTTP 请求，再停止后台生产者、下载及播放任务，最后等待整理日志落库、关闭数据库。
- 如果 HTTP 排空超过调用方期限，返回错误并保持数据库连接，避免仍在运行的请求访问已关闭数据库。
- 预整理被停机中断时回到待处理状态，下次启动继续；播放任务仍保持串行、原有间隔及最多三次尝试，并在停止返回前完成落库。

## 自动化历史

历史列表仍按条目 ID 倒序分页，“最新运行”仍按最大运行 ID 判断。SQL 只加载每个条目的最新运行，继续返回该运行的全部原有字段；匹配筛选仍考虑旧运行，历史及工作流快照不会被删除。

播放队列状态统计使用一次 SQL 分组聚合，继续返回原有四个状态键和零值。

## 回归检查

```sh
go test ./...
go test -race ./...
go vet ./...
```

针对性测试覆盖并发入队去重、停止等待、重试次数、请求取消、令牌缓存共享、SQLite 并发写入、停机期间数据库可用性、历史查询读取数量，以及既有整理预览、执行和洗版恢复行为。

第二轮回归覆盖目录缓存并发读写、容量/过期/副本语义、MoviePilot 并发登录合并与 401 终止、115 多账号并发凭据及缓存隔离，以及播放队列单次聚合查询。

代码布局调整逐个比对移动前后的 Go 声明，检查函数实现、数据结构、常量和变量初始化表达式保持一致，再运行上述完整回归。文件拆分本身用于降低维护成本，不作为运行性能提升的依据。
