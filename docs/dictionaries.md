# 通用数据字典

新版后台入口为“系统管理 → 数据字典”。实现参考 P0001 的类型/字典项分层、稳定编码、启停与版本校验，并沿用 FilmFusion 的管理员 JWT、GORM/SQLite 及响应格式。

## 管理规则

- 后台只维护系统提供的字典类型，支持查询、编辑名称/说明和启停，不开放创建类型入口；编码不可改，不提供删除类型。类型随业务功能由系统初始化，手动新建类型不会自动接入业务页面。
- 字典项支持创建、查询、编辑标签/说明/排序/状态和删除；值在类型内唯一且创建后固定。通用值为字符串，保留 `001` 等前导零。
- 类型编码匹配 `[a-z][a-z0-9_.-]{0,127}`；名称/标签最多 100 字，说明最多 500 字，项值最多 128 字。必填字段不接受首尾空白或空字符。
- 排序号为 -1,000,000 到 1,000,000 的整数，小值优先；允许负值以保留旧 115 目录的排序。日期、名称、编码/值等排序均在服务端对完整筛选结果执行后分页。
- 创建默认启用；停用类型不改变项状态，但业务候选接口明确返回 `DICTIONARY_DISABLED`。维护页面仍可查看和编辑停用类型/项。
- 表格支持双击编辑可写字段；独立编辑表单打开时读取最新详情，并固定首次读取版本。冲突保留草稿，关闭重开确认后再修改，不自动提升版本重试。
- `version` 初始为 1；PATCH 和 DELETE 必须携带 `expectedVersion`。版本不一致返回 HTTP 409，条件更新和删除在事务中完成。

当前只有 115 应用候选接入。候选值不构成业务外键或授权白名单；删除/停用项不会修改已有网盘的 `app_id` 或凭据。将来其他业务需要持久引用字典项时，须同时增加事务内引用校验，不能直接沿用候选删除规则。

通用字典表、服务和后端创建接口继续保留，供后续业务扩展；新版前端不再提供类型创建表单或调用类型创建接口，字典项维护能力不变。

## 接口

以下路径均位于 `/api`，只允许后台管理员登录 JWT；现有 API Token 不能访问通用字典。响应沿用 `{code,message,data}`，成功 `code=0`，列表 `data={items,total,page,pageSize}`。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET / POST | `/dictionaries` | 类型列表 / 新建 |
| GET / PATCH | `/dictionaries/:code` | 类型详情 / 更新 |
| GET / POST | `/dictionaries/:code/items` | 项列表 / 新建 |
| GET / PATCH / DELETE | `/dictionaries/:code/items/:id` | 项详情 / 更新 / 删除 |
| GET | `/dictionary-options/:code` | 启用项的 `{value,label}` 候选 |

管理列表参数：`page`（默认 1）、`pageSize`（默认 20，1–100）、`search`（最多 100 字）、`enabled=true|false`、`sort`、`order=asc|desc`。类型可排序字段：`code,name,createdAt,updatedAt`；项可排序字段：`value,label,sortOrder,createdAt,updatedAt`。搜索按字面子串匹配，`%` 不作为通配符。

候选接口支持 `page/pageSize/search`，以及重复 `ids`（最多 100 个）用于按值精确回显；传入 `ids` 时忽略搜索和分页，只返回匹配的启用项。名称/值搜索不包含说明。

PATCH 请求体示例：`{"expectedVersion":1,"label":"应用名称","sortOrder":0,"enabled":false}`。DELETE 版本放在查询参数 `?expectedVersion=1`。输入拒绝未知字段、显式 null、超出 64 KiB 的 JSON；稳定编码/值不能在 PATCH 中传入。业务错误 `data.reason` 为 `INVALID_INPUT`、`NOT_FOUND`、`DUPLICATE`、`VERSION_CONFLICT` 或 `DICTIONARY_DISABLED`，前端按原因翻译。

## 115 迁移与兼容

`cloud.115_apps` 是唯一维护来源。后端启动自动创建 `dictionaries` / `dictionary_items` 表，并在事务中迁移旧 `system_configs` 中同名 JSON 配置；保留自定义名称、启停、排序、空数组，软删除的旧配置迁为停用类型。成功后删除旧配置；无效旧数据会中止迁移，不静默恢复内置名单。已有通用类型优先，重复启动不会补回已删除项。

没有旧目录的新安装使用内置 1,075 项公开候选。新版 115 授权读取 `/dictionary-options/cloud.115_apps`；旧 `/auth/115/apps` 兼容入口继续读取同一字典，并保留其 `cloud-storage` Token 范围及 snake_case 分页格式。旧版手动授权接口不变。升级应同时更新后端和 V2 前端，部署前按现有流程备份 SQLite 数据库。
