# 自动化工作流：115 OpenAPI 云下载

依据 2026-10-03 读取的 [115 开放平台文档](https://open.115.com/developer-doc) 对接。文档实际入口为 `https://open.115.com/doc/`，云下载目录共有下列七个接口。验证使用内存 HTTP Transport 和本地界面预演，没有调用真实 115 API。

| 工作流节点 | 类型标识 | API 路径（`/open/offline/`） | 输入 |
| --- | --- | --- | --- |
| 添加链接任务 | `offline115_openapi`（兼容已有流程） | POST `add_task_urls` | 下载链接、保存目录 |
| 解析 BT 种子 | `torrent115_openapi` | POST `torrent` | `torrent_sha1`、`pick_code` |
| 添加 BT 任务 | `offline115_bt_openapi` | POST `add_task_bt` | `info_hash`、`wanted`、`save_path`、`torrent_sha1`、`pick_code`、保存目录 |
| 云下载任务列表 | `list115_tasks_openapi` | GET `get_task_list` | `page`；可选 `info_hash` 为客户端筛选条件 |
| 云下载配额 | `quota115_openapi` | GET `get_quota_info` | 无额外参数 |
| 删除云下载任务 | `delete115_task_openapi` | POST `del_task` | `info_hash`、是否删除源文件 |
| 清空云下载任务 | `clear115_tasks_openapi` | POST `clear_task` | `flag` 清理范围 |

请求使用所选账号的 Bearer AccessToken；POST 使用文档规定的 multipart/form-data，GET 参数放入 URL query。工作流输出不包含认证信息。

## 链接任务与 BT 任务

RSS 提供磁力链、HTTP(S)、FTP 或电驴下载链接时，使用“添加链接任务”。可以换行提交多条链接，单次最多 115 条。保存配置仍使用 `url`，发送给 115 时映射为 `urls`。

“添加 BT 任务”依赖已上传到当前授权账号的种子文件。先用种子文件的 `torrent_sha1` 和 `pick_code` 解析，得到 BT `info_hash`、名称和文件列表，再添加任务。种子文件 SHA1 与 BT info_hash 不可混用。本次没有新增种子文件上传功能。

RSS 返回 `.torrent` 下载地址时，也可以先接 [本地种子转磁力节点](automation-torrent-to-magnet.md)，再将它的 `magnet` 输出交给“添加链接任务”。这条路径不上传种子，支持 v1 和混合种子；私有种子能否下载取决于下载器能否取得元数据和访问 Tracker。

`wanted` 是以逗号分隔的文件索引，例如 `0,2`，`0` 有效。解析节点提供默认选中索引；若解析结果没有默认选中项，需自行填写。`save_path` 是相对于保存目录的路径。

## 响应变量

全部 OpenAPI 云下载节点及重命名节点保留：

- `state`、`code`、`message`、`data`：115 原始响应字段。
- `response`：原始响应对象，包含未被提升为常用变量的扩展字段。
- `cloud_storage_id`、`storage_name`、`access_method`：本次使用的账号信息。

添加链接任务额外提供 `results`、`failed_results`、`hashes`、`success_count`、`failure_count`、`partial`。只有一个链接成功时提供 `info_hash`。部分成功走失败出口并保留所有逐条结果，不能将顶层 `state=true` 当作每条链接都成功。

BT 添加接口成功时 `data=[]`，节点的 `info_hash` 和 `hashes` 来自成功提交的输入。提交、删除和清空不会自动重试；旧链接节点即使保存了较高重试次数，运行时也只尝试一次，避免部分成功或网络结果不明时重复产生副作用。

嵌套字段可用点路径引用，例如：

```text
$nodes.add.output.data.0.info_hash
$nodes.list.output.task.percentDone
$nodes.list.output.task.delete_file_id
$nodes.quota.output.data.package.0.expire_info
```

也可以把 `data` / `response` 交给 JSON 取值节点。节点变量面板列出原始对象和常用字段；预演值标记为模拟值。

## 根据 info_hash 重命名

推荐连接：

```text
RSS → 添加链接任务 → 等待 115 下载完成 → 115 API 重命名
RSS → 种子转磁力 → 添加链接任务 → 等待 115 下载完成 → 115 API 重命名
种子解析 → 添加 BT 任务 → 等待 115 下载完成 → 115 API 重命名
```

等待节点自动继承上游账号和任务 hashes，持续查询任务列表。重命名节点选择相同账号，文件 ID 填 `$nodes.wait.output.file_id`，新名称可以填 `{{item.title}}.mkv`（`wait` 应替换为实际节点 ID）。

已有任务也可以使用“任务列表”节点，将 `info_hash` 设置为提交节点的输出或已知 Hash。此时从第一页跨页精确匹配，最多 200 页；`page` 只在未指定 Hash 时使用。配置了 Hash 的任务列表节点可以继续连接等待节点。

仅当任务 `status=2` 且返回有效 `file_id` 时，常用输出 `completed=true`、`file_id` 才可用于重命名。找不到任务时 `matched=false`；未完成时 `file_id` 为空。原始 `task.file_id` 保持服务端原样，自动重命名应使用经过完成判断的顶层 `file_id`。

`info_hash` 标识下载任务，重命名 API 接受文件或文件夹 ID，不能直接把 Hash 传给重命名。多文件种子的任务 ID 可能对应文件夹，重命名它会修改文件夹名；逐个重命名内部视频还需要获取其各自的文件 ID。多任务等待提供 `file_ids`，不会从多个任务中随意挑选一个作为单文件重命名目标。

## 删除与清空

删除默认仅删除指定任务并保留源文件。清空默认 `flag=0`，只清理已完成任务并保留文件。清空作用于账号全局，不限于当前工作流；`flag=4/5` 会同时删除对应源文件，配置界面明确显示这一影响。

## 验证边界

测试覆盖请求方法、multipart 字段、鉴权、响应透传、逐链接失败、跨页匹配、等待完成条件、BT 到重命名的整条链路、配置保存、变量预演和导入导出。模拟验证不等于真实账号已提交任务、已下载或已重命名；本次未进行这些真实操作。
