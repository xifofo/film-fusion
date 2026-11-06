# 内置媒体识别与整理

## 引擎与兼容

识别、搜索、分类和命名统一使用 `medianame`：识别词 → medianame 解析与 TMDB 查询 → 分类规则 → Go 模板命名。手写 local 解析器、旧候选评分和 Jinja 模板引擎已移除。

历史 `local` / `filmfusion` / `moviepilot` / `mp2` / `mp` / `shadow` 配置在读取或接收请求时兼容为 `medianame`，保存后使用规范值；启动时将系统配置中的历史引擎值写回 `medianame`。两版管理界面的系统设置均移除“媒体识别”页签，自动化不再提供引擎选择或参数绑定；识别词、分类、命名与测试仍在独立的媒体识别工作台维护。已有媒体不会自动重整，旧 Jinja 模板不会自动转成 Go 模板。

已保存的 local、MP2 或影子整理计划必须重新预览，服务端在创建 115 客户端前拒绝执行；medianame 计划继续使用服务器保存的路径快照。

自动化节点使用 `media_title_recognize`（标题）和 `media_file_recognize`（115 文件）。历史 `moviepilot_title_recognize`、`moviepilot_recognize` JSON 在读取时兼容转换，保留节点 ID、连线、参数绑定和失败出口。旧 `filmfusion_recognize` 节点也复用 medianame，保留标题/文件模式和是否查询 TMDB 的行为；有歧义或冲突的 TMDB 结果走失败出口。

MP2 手动整理入库、Webhook 与 RSS 中转等独立集成继续保留，它们不是 FilmFusion 的识别引擎。内部部分 `MoviePilot*` 类型名和历史快照 JSON 字段仅为兼容保留；它们不代表仍调用远端识别。

## 配置与路径

识别词作为 medianame 解析前处理保存于 `media_recognition.words`。分类与命名统一保存于 `media_recognition.medianame.v1`，包含 `category_yaml`、`movie_template`、`tv_template`、`version`；首次使用插入包的默认配置，升级不会覆盖已有模板。旧 `media_recognition.categories` 和 `media_recognition.rename_formats` 行不再参与执行，也不提供读写接口。

最终路径为「媒体库根目录 / 分类（启用时）/ 模板输出」。分类由 FilmFusion 拼接，模板负责媒体目录、季目录和文件名，不在模板中使用 `category`。

medianame 使用输入中的显式 TMDB ID、外部 ID 和名称候选；只有包返回可兼容身份时生成执行计划。冲突、歧义和请求失败保留诊断，不以旧评分或 MP2 结果替换候选。TMDB 客户端沿用 FilmFusion 的启用状态、地址、凭据、超时及缓存；计划快照不保存凭据。

## 工作台与整理

- 媒体识别页仅提供 medianame 完整流程和共用识别词。分类与命名直接在 medianame 配置中编辑。
- 完整流程预览固定使用 medianame，支持配置草稿，只生成识别和目标路径。
- 目录配置、预整理队列和整理确认不再提供引擎选择，自动沿用配置；确认执行使用已有预览快照，重新预览仍需核对所选文件的目标路径。所有入口统一使用 medianame。
- 自动化只预览时保留 `plans`、`items`、诊断与计划数量，实际整理和 STRM 数量仍为 0。

`POST /api/organize/115-cookie` 首次调用使用 `dry_run: true`，响应包含 `plan_id` 和冻结的 `pipeline`。确认提交该 `plan_id`、原选项及可选文件子集，`dry_run: false`。客户端不能替换目标路径。源文件、目录条件、选项、授权或计划状态不符时要求重新预览；同一计划不能重复执行。

## 候选选择

`POST /api/organize/medianame-candidate` 接收 `plan_id`、`file_ids`、`tmdb_id`、`media_type` 和可选 `preview_task_id`。候选必须来自服务器保存的计划，电影和剧集按「类型 + TMDB ID」区分。可选择当前文件，或同源目录内有相同候选的文件；每个文件保留自己的季集信息。

手选结果标记 `selection_basis: manual` 并保留原始冲突；生成新的 medianame 计划、字幕关联、STRM 及版本判断。此操作只预览，不写网盘。连续选择保留之前的身份；通过 `candidate_plan_id` 继承手选结果时，源文件、识别词或包版本变化会要求重新识别。

## 接口

| 接口 | 用途 |
| --- | --- |
| `GET/PUT /api/media-recognition/medianame-config` | 读取或保存独立配置 |
| `POST /api/media-recognition/medianame-config/validate` | 校验分类与模板 |
| `POST /api/media-recognition/medianame-config/preview` | 模板变量预览 |
| `POST /api/media-recognition/pipeline-test` | 与整理共用计划构建器，不访问 115 |
| `GET/PUT /api/media-recognition/words` | 读取或保存共用识别词 |
| `POST /api/media-recognition/test` | 使用已保存或临时词表执行 medianame 识别 |
| `GET /api/organize/category-config` | 返回 medianame 分类 |

完整流程输入示例：

```json
{"input":"/Example.2024.[tmdb=123]/Season 0/01.mkv","mode":"file","source":"medianame","root":"影视","classify":true}
```

可选 `medianame_config` 是临时草稿，不写入保存配置。所有接口沿用原登录与权限要求。

## 2026-10-23 片名与完结范围更新

当前依赖为 `github.com/xifofo/medianame v0.1.3`，通过 Go Module 获取正式版本，无本机 `replace`。识别响应、默认配置和新整理计划中的包版本同步为 `v0.1.3`。

新版支持将独立的中文、英文片名拆成分别可查询的名称，例如 `凡人修仙传.The.Immortal.Ascension.2020.S01E05.mkv`；支持 `01-26Fin`、`01-26 End`、`01-26完结` 等完结范围，在没有明确集号时补充第 1–26 集。已有分类、命名模板和默认引擎配置继续保留，已有整理计划仍使用其保存的路径；要采用新版解析结果需重新预览。

## 2026-10-10 命名字段更新

该次依赖升级为 `github.com/xifofo/medianame v0.1.2`。`edition`、`effect`、`audioCodec`、`resource_term` 由包直接生成点号分隔的命名文本，Dolby Vision 在命名时缩写为 DV。独立的 HDR、HDR10、HDR10+ 标签保留，不根据 DV 推断或补充 HDR。Atmos 保留在 `effect` / `edition`，音轨不再重复附加。原始解析数据仍使用标准名称。

例如 `Kingdom.of.Heaven.2005.2160p.Directors.Cut.Roadshow.Version.UHD.BluRay.REMUX.HEVC.DV.HDR.TrueHD.Atmos.7.1-HDHIVE.mkv` 的默认命名为 `Kingdom of Heaven.2005.REMUX.DV.HDR.Atmos.2160p.H.265.TrueHD.7.1-HDHIVE.mkv`。

已有配置和模板不会被覆盖，直接引用这些字段的模板无需添加 `replace`；只使用 `audioCodec` 的自定义模板若要保留效果标签，需要加入 `effect` 或 `edition`。升级后新生成的预览采用新命名；已保存的整理计划仍执行其快照路径，需要重新预览才能使用新格式。
