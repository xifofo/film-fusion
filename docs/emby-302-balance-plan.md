# Emby 302 多账号负载均衡计划

## 1. 背景与目标

当前 FilmFusion 已有 Emby 代理 302 能力：播放请求命中 `Match302` 后，根据 Emby 实际媒体路径映射到 115 路径，再用绑定的 115 账号获取直链并 302 给播放器。

本计划目标是在现有链路上增加多账号负载均衡：

- 源账号也参与播放，不只是回退账号。
- 子账号默认没有资源，需要按需通过 115-to-115 秒传生成子账号自己的文件和 `pickcode`。
- 播放时按固定分配使用源账号或子账号获取 302 直链。
- 页面能看到当前播放、账号负载、秒传状态、回退原因和子账号缓存清理情况。
- 子账号里的资源视为 FilmFusion 为 302 负载均衡生成的缓存资源，需要有自动清理机制。

## 2. 总体流程

### 2.1 详情页预热流程

1. Emby 详情页请求进入 FilmFusion Emby 代理。
2. 代理从 Emby 响应或主动查询中拿到媒体 `item_id`、`media_source_id`、真实媒体路径。
3. 媒体路径命中某条 `Match302` 规则。
4. 如果该规则未开启负载均衡，只保留现有逻辑。
5. 如果开启负载均衡：
   - 获取源账号文件信息和源 `pickcode`。
   - 按媒体固定选择一个播放账号。
   - 如果选中源账号，分配记录直接置为 `ready`。
   - 如果选中子账号，后台启动 115-to-115 秒传任务。
6. 详情页响应不等待秒传完成，避免拖慢 Emby 页面。

### 2.2 播放请求流程

1. 播放流请求命中 `/Videos/.../(stream|original|master)`。
2. 解析 Emby 媒体路径并匹配 `Match302`。
3. 判断该规则是否开启负载均衡。
4. 如果未开启，走现有源账号 302。
5. 如果开启：
   - 查询该媒体的固定分配记录。
   - 如果分配账号是源账号，直接用源账号 `pickcode` 获取直链。
   - 如果分配账号是子账号且状态为 `ready`，用子账号 `target_pickcode` 获取直链。
   - 如果子账号状态为 `pending` 或 `transferring`，短暂等待。
   - 等待后仍未 `ready`，临时回退源账号播放，但不改变固定分配。
   - 如果子账号状态为 `failed`，记录原因并临时回退源账号播放。

## 3. 何时进入负载均衡

只有同时满足以下条件才进入负载均衡：

- 播放路径命中某条 `Match302`。
- 该规则 `balance_enabled = true`。
- 源账号可用，且有 115 Web Cookie。
- 候选池至少有一个可用账号。
- 当前媒体能解析出源账号文件信息或源 `pickcode`。

不满足时保持现有源账号 302 或默认反代逻辑。

页面状态需要明确展示不进入负载均衡的原因，例如：

- 未命中 Match302。
- 未启用负载均衡。
- 源账号 Cookie 缺失。
- 无可用子账号。
- 源文件 pickcode 解析失败。
- 子账号处于冷却期。

## 4. 账号候选池与选择策略

### 4.1 候选池

候选播放账号由两部分组成：

- Match302 绑定的源账号。
- Match302 配置的已启用子账号池。

源账号不写入子账号池表，而是在选择器中作为虚拟候选加入，避免重复建模。

### 4.2 源账号行为

源账号同时承担两个角色：

- 资源来源账号：给子账号秒传时作为 `from_client`。
- 可播放账号：被负载均衡选中时直接播放。

如果分配到源账号：

- 不触发秒传。
- 分配状态直接为 `ready`。
- `target_pickcode = source_pickcode`。
- 播放时使用源账号直链。

### 4.3 子账号行为

如果分配到子账号：

- 子账号默认没有资源。
- 需要用源账号作为 `from_client`，子账号作为 `to_client`。
- 通过 Go 版 p115client 115-to-115 秒传能力把资源物化到子账号。
- 成功后记录子账号文件 ID、目标路径和 `target_pickcode`。
- 播放时使用子账号 `target_pickcode` 获取直链。

### 4.4 选择策略

v1 使用 `sticky_least_active`：

- 按媒体固定分配。
- Cookie 缺失账号跳过。
- disabled 账号跳过。
- cooldown 中账号跳过。
- 活跃播放数越少越优先。
- 权重越高越容易被选中。

建议评分方式：

```text
score = active_playbacks / max(weight, 1)
```

选择 `score` 最小的账号；并列时按 `sort_order`、账号 ID 稳定排序。

### 4.5 固定分配

同一媒体只分配一次：

- 唯一键：`match302_id + source_file_path`。
- 已有 `ready` 分配时直接复用。
- 已有 `pending/transferring` 分配时不创建第二个任务。
- 临时回退源账号不改变固定分配。

## 5. Go 版 p115client 115-to-115 秒传服务

### 5.1 定位

该服务是 FilmFusion 内部能力，不是通用手动迁移功能。

使用场景：

- 302 负载均衡分配到子账号时，将源账号资源按需秒传到子账号。

### 5.2 内部接口

建议接口：

```go
type Transfer115To115Options struct {
    FromClient    *P115WebClient
    ToClient      *P115WebClient
    FromCID       string
    ToPID         string
    MaxWorkers    int
    WithRoot      bool
    UseIterFiles  bool
    RequestKwargs map[string]string
}

type TransferResult struct {
    Type       string // good | fail | skip
    SourceAttr P115FileAttr
    TargetPID  string
    TargetFile *P115FileAttr
    Resp       map[string]any
    Err        error
}
```

Go 中不保留 Python 的 `async` 参数，使用：

- `context.Context`
- goroutine
- worker pool
- result channel

### 5.3 参数语义

保持 p115client `iter_115_to_115` 语义：

- `from_client`: 来源 115 客户端对象。
- `to_client`: 去向 115 客户端对象。
- `from_cid`: 来源 115 的目录 id 或 pickcode。
- `to_pid`: 去向 115 的父目录 id 或 pickcode。
- `max_workers`: 最大并发数。
- `with_root`: 是否保留 `from_cid` 对应的目录名。
- `use_iter_files`: `true` 走 `iter_files_with_path` 风格；`false` 走 `iter_download_files` 风格。
- `request_kwargs`: 内部请求附加参数。

默认值：

- `max_workers = 8`
- `with_root = true`
- `use_iter_files = false`

### 5.4 需要翻译的核心能力

最小实现范围：

- 115 Web Cookie 鉴权。
- `id/pickcode` 互转和合法性识别。
- `iter_download_files` 风格文件遍历。
- `iter_files_with_path` 风格文件遍历。
- `fs_supervision` 补齐源文件信息。
- `fs_makedirs_app` 创建目标目录。
- `download_url` 获取源账号直链。
- `upload_file_init` 发起目标账号秒传。
- `status=7` 时用源账号直链 Range 读取并提交 `sign_val`。
- 上传响应解密和结果解析。

### 5.5 秒传结果

结果类型保持 p115client 语义：

- `good`: 秒传成功或复用成功。
- `fail`: 秒传失败。
- `skip`: 跳过，例如特殊资源或不支持的资源。

对于 `is_collect && size >= 115MB`，保持 p115client 行为，返回 `skip`。

## 6. 目标目录与复用策略

### 6.1 默认目录

子账号默认目标根目录：

```text
/FilmFusion-302/{match302_id}
```

账号池成员允许覆盖 `target_root_path`。

### 6.2 实际路径

实际目标路径：

```text
target_root_path / 原相对目录 / 原文件名
```

### 6.3 已存在文件复用

如果目标路径已有同名文件：

- 优先确认 sha1 和 size。
- 一致则复用该文件的 `pickcode`。
- 不重复制造副本。
- 不一致则记录冲突错误，不覆盖用户已有文件。

### 6.4 删除保护边界

后续清理只能删除 FilmFusion 创建并记录过的目标文件，不能扫描目标目录批量删除未知文件。

## 7. 数据模型

### 7.1 扩展 `match_302`

新增字段：

```text
balance_enabled      bool
balance_strategy     string // v1: sticky_least_active
source_weight        int
cleanup_enabled      bool
retention_hours      int
cleanup_mode         string // v1: recycle
cleanup_interval_min int
min_keep_ready       int
```

默认值：

- `balance_enabled = false`
- `balance_strategy = sticky_least_active`
- `source_weight = 1`
- `cleanup_enabled = true`
- `retention_hours = 72`
- `cleanup_mode = recycle`
- `cleanup_interval_min = 30`
- `min_keep_ready = 0`

### 7.2 新增 `match302_balance_members`

字段：

```text
id
match302_id
cloud_storage_id
enabled
weight
target_root_path
last_error
last_error_at
cooldown_until
created_at
updated_at
```

约束：

- 唯一键：`match302_id + cloud_storage_id`。
- `cloud_storage_id` 必须是 `115open` 类型账号。
- 源账号不写入该表。

### 7.3 新增 `match302_balance_assignments`

字段：

```text
id
match302_id
emby_item_id
media_source_id
source_file_path
source_storage_id
playback_storage_id
is_source_playback
source_pickcode
target_pickcode
source_file_id
target_file_id
target_path
sha1
size
status
attempts
last_error
last_error_at
last_ready_at
last_played_at
expires_at
cleanup_status
cleanup_error
cleaned_at
created_at
updated_at
```

状态：

```text
pending
transferring
ready
failed
```

清理状态：

```text
none
pending
cleaning
cleaned
failed
```

约束：

- 唯一键：`match302_id + source_file_path`。
- 源账号分配：`is_source_playback = true`，`target_pickcode = source_pickcode`。
- 子账号分配：`is_source_playback = false`，需要记录 `target_file_id` 和 `target_pickcode`。

## 8. 并发与幂等

### 8.1 分配锁

多个客户端同时进入同一个详情页或同时播放同一媒体时：

- 使用唯一键保证只创建一条 assignment。
- `pending/transferring` 状态作为任务锁。
- 已存在任务时直接复用，不重复秒传。

### 8.2 秒传锁

同一 assignment 只能有一个活跃秒传任务。

建议通过数据库状态更新做 CAS：

```text
UPDATE assignments
SET status = 'transferring', attempts = attempts + 1
WHERE id = ? AND status IN ('pending', 'failed')
```

更新成功才启动任务。

### 8.3 播放回退

回退只影响本次播放：

- 记录事件。
- 看板展示实际播放账号为源账号。
- assignment 仍保持原来的子账号分配。
- 后续可继续重试子账号秒传。

## 9. 播放直链缓存

负载均衡后，直链缓存 key 必须包含账号维度。

推荐 key：

```text
actual_storage_id + pickcode + user_agent
```

不能再只按 URI 和 User-Agent 缓存，否则源账号直链和子账号直链可能串用。

缓存过期时间仍使用现有 Emby `cache_time`，但建议从 115 URL 的 `t` 参数中取更短者。

## 10. 当前播放与负载看板

### 10.1 页面位置

升级现有 `/emby-proxy-log` 页面，不新增独立页。

页面结构：

- 顶部：实时看板。
- 中部：账号负载和秒传队列。
- 下方：现有 302 日志表，增加负载均衡字段。

### 10.2 当前播放判断

v1 从代理请求流推断，不调用 Emby Sessions API。

判断规则：

- 最近 2 分钟有播放流请求：`active`。
- 2 到 15 分钟：`recent`。
- 超过 15 分钟：从看板移除。

合并 key：

```text
item_id + media_source_id + remote_ip + user_agent
```

### 10.3 当前播放展示字段

每条当前播放显示：

- 媒体路径。
- Emby ItemID。
- MediaSourceID。
- 客户端 IP。
- User-Agent。
- 命中的 Match302。
- 固定分配账号。
- 实际播放账号。
- 账号类型：源账号 / 子账号。
- 状态：源账号播放、子账号播放、秒传中、失败回退。
- 最近请求时间。
- 回退原因。

### 10.4 账号负载展示字段

源账号和子账号都展示：

- 账号名称。
- 账号类型。
- 当前 active 播放数。
- 总分配数。
- ready 数。
- pending 数。
- transferring 数。
- failed 数。
- cooldown 状态。
- 最近成功时间。
- 最近失败时间。

### 10.5 秒传队列展示字段

展示：

- 媒体路径。
- 源账号。
- 目标子账号。
- 状态。
- 尝试次数。
- 错误原因。
- 创建时间。
- 最近更新时间。
- 操作：重试秒传。

### 10.6 回退统计

展示：

- 最近回退次数。
- 按原因分布。
- 最近回退事件。

原因示例：

- 子账号秒传未完成。
- 子账号秒传失败。
- 子账号 Cookie 缺失。
- 子账号处于 cooldown。
- 目标 pickcode 缺失。
- 获取子账号直链失败。

## 11. 子账号资源清理机制

### 11.1 资源性质

子账号里的文件是 FilmFusion 为 302 负载均衡生成的缓存资源。

清理原则：

- 源账号永远不删。
- 只删除 FilmFusion 创建并记录的子账号目标文件。
- 不扫描目标目录乱删。
- v1 默认移动到回收站，不做永久删除。

### 11.2 清理配置

每条 Match302 规则可配置：

```text
cleanup_enabled
retention_hours
cleanup_mode
min_keep_ready
cleanup_interval_min
```

默认：

- 自动清理开启。
- 最后播放后保留 72 小时。
- `cleanup_mode = recycle`。
- 不强制保留 ready 资源。
- 每 30 分钟扫描一次。

### 11.3 删除条件

只有同时满足以下条件才清理：

- 分配到的是子账号，不是源账号。
- 目标文件由 FilmFusion 创建并记录。
- `target_file_id` 或 `target_pickcode` 可确认。
- 文件位于该子账号配置的 `target_root_path` 下。
- 当前没有 active 播放。
- 没有正在秒传、重试或获取直链。
- `last_played_at + retention_hours < now`。
- `cleanup_status` 不是 `cleaning`。

### 11.4 播放期间保护

清理前必须再次检查当前播放状态：

- 最近 2 分钟内有播放流请求，不删除。
- 当前播放看板中 active 的 assignment 不删除。
- 正在处理的 `pending/transferring` 不删除。

### 11.5 清理服务

新增 `BalanceCleanupService`：

- 周期扫描过期 assignment。
- 按目标账号分组。
- 每次限制清理数量，避免大量删除触发风控。
- 删除成功后标记 `cleaned`。
- 删除失败后标记 `failed`，保留错误原因。
- 页面提供手动重试清理。

### 11.6 页面展示

看板新增“子账号缓存资源”：

- 当前缓存数量。
- 即将过期数量。
- 清理失败数量。
- 最近清理时间。
- 每个账号缓存资源数量。
- 单条操作：
  - 延长保留。
  - 立即清理。
  - 重试清理。

## 12. API 设计

### 12.1 扩展 Match302 API

`GET/POST/PUT /api/match-302`

新增字段：

```json
{
  "balance_enabled": true,
  "balance_strategy": "sticky_least_active",
  "source_weight": 1,
  "cleanup_enabled": true,
  "retention_hours": 72,
  "cleanup_mode": "recycle",
  "cleanup_interval_min": 30,
  "min_keep_ready": 0,
  "pool_members": [
    {
      "cloud_storage_id": 2,
      "enabled": true,
      "weight": 1,
      "target_root_path": "/FilmFusion-302/1"
    }
  ]
}
```

### 12.2 看板接口

```text
GET /api/emby-proxy/balance-status
```

返回：

```json
{
  "active_playbacks": [],
  "account_loads": [],
  "transfer_summary": {},
  "cleanup_summary": {},
  "recent_fallbacks": [],
  "recent_events": []
}
```

### 12.3 Assignment 接口

```text
GET /api/match-302/:id/assignments
POST /api/match-302/:id/assignments/:assignment_id/retry
POST /api/match-302/:id/assignments/:assignment_id/cleanup
POST /api/match-302/:id/assignments/:assignment_id/extend-retention
```

### 12.4 不做的接口

v1 不新增通用手动 115-to-115 迁移 API。

原因：

- 本功能中的 115-to-115 是 302 内部按需秒传能力。
- 手动目录搬运会引入独立任务生命周期、权限边界和 UI，不属于 v1 目标。

## 13. 前端改动

### 13.1 Match302 配置页

新增：

- 是否启用负载均衡。
- 源账号权重。
- 子账号池选择。
- 子账号权重。
- 子账号目标根目录。
- 清理策略配置。

### 13.2 Emby 代理日志页

升级为实时看板：

- 当前播放。
- 账号负载。
- 秒传队列。
- 子账号缓存资源。
- 回退统计。
- 302 明细日志。

现有自动刷新保留，刷新间隔仍可使用 3 秒。

### 13.3 状态标签

建议标签：

- `源账号播放`
- `子账号播放`
- `秒传中`
- `等待秒传`
- `失败回退`
- `未启用负载均衡`
- `无可用账号`
- `待清理`
- `已清理`
- `清理失败`

## 14. 后端改动

### 14.1 新增服务

- `BalanceAssignmentService`
- `BalanceSelectorService`
- `Transfer115Service`
- `BalanceStatusService`
- `BalanceCleanupService`

### 14.2 Emby 代理改造

改造点：

- 详情页/媒体列表响应中识别媒体并异步预热。
- 播放请求中查询分配并决定实际播放账号。
- 记录播放事件和回退事件。
- 扩展 302 日志结构。
- 调整直链缓存 key。

### 14.3 115 Web 客户端

新增内部 `p115web` 包：

- Cookie 规范化和校验。
- 115 Web/Pro API 请求。
- RSA/AES/LZ4 相关加解密。
- 上传初始化参数构造。
- Range 读取源直链。

## 15. 风险与约束

### 15.1 Cookie 风控

源账号和子账号都需要有效 115 Web Cookie。

风险：

- Cookie 过期。
- 请求频率过高触发风控。
- 子账号频繁秒传或删除触发限制。

缓解：

- cooldown。
- 每账号并发限制。
- 清理限速。
- 页面明确展示 Cookie 缺失/失效。

### 15.2 秒传不保证成功

不是所有文件都一定能秒传成功。

缓解：

- 失败记录。
- 手动重试。
- 临时回退源账号播放。

### 15.3 当前播放推断不等于真实播放状态

v1 从代理请求流推断，不调用 Emby Sessions API。

结果：

- 暂停状态无法准确识别。
- 某些播放器缓冲完成后可能不再频繁请求。

缓解：

- 使用 active/recent TTL。
- 页面文案标注为代理请求推断状态。

## 16. 测试计划

### 16.1 Go 单测

覆盖：

- Match302 命中和路径映射。
- 候选账号生成。
- 源账号虚拟候选。
- 权重和活跃播放数选择。
- disabled/Cookie 缺失/cooldown 跳过。
- assignment 唯一键和并发创建。
- 源账号分配不触发秒传。
- 子账号分配触发秒传。
- `upload_file_init status=2` 成功。
- `upload_file_init status=7` Range 校验成功。
- 秒传失败进入 `failed`。
- 临时回退不修改固定分配。
- 直链缓存不串账号。
- 当前播放 active/recent/expired TTL。
- 清理条件判断。
- active 播放保护。
- 清理成功和失败状态。

### 16.2 前端验证

执行：

```bash
npm run lint
```

验证：

- Match302 负载均衡配置可创建和编辑。
- 实时看板空状态正确。
- 当前播放显示分配账号和实际播放账号。
- 秒传中、失败回退、子账号播放状态可区分。
- 清理失败可见并可重试。

### 16.3 后端验证

执行：

```bash
go test ./...
```

手动场景：

- 只启用源账号，播放正常。
- 源账号和一个子账号，媒体分到源账号时不秒传。
- 媒体分到子账号时触发秒传。
- 子账号 ready 后播放走子账号。
- 子账号秒传中播放临时回退源账号。
- 子账号 Cookie 失效后跳过或回退。
- 超过保留时间后清理子账号资源。
- active 播放期间不会清理。

## 17. v1 不做

- 不做通用手动 115-to-115 迁移页面。
- 不做永久删除，默认只进回收站。
- 不做 Emby Sessions API 精准播放状态。
- 不做自动获取 115 Web Cookie。
- 不做跨规则共享子账号资源去重。

## 18. 实施顺序

1. 数据模型和迁移。
2. 115 Web 客户端最小能力。
3. Go 版 115-to-115 内部秒传服务。
4. 负载均衡选择和 assignment 服务。
5. Emby 代理播放链路接入。
6. 详情页异步预热。
7. 直链缓存 key 改造。
8. 302 日志结构扩展。
9. 实时看板 API。
10. 前端 Match302 配置。
11. 前端实时看板。
12. 子账号资源清理服务。
13. 清理状态和手动重试 UI。
14. 单测、前端 lint、端到端手动验证。
