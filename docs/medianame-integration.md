# medianame 并行引擎接入

## 2026-10-10 命名字段更新

当前依赖升级为 `github.com/xifofo/medianame v0.1.2`。`edition`、`effect`、`audioCodec`、`resource_term` 由包直接生成点号分隔的命名文本，Dolby Vision 在命名时缩写为 DV。独立的 HDR、HDR10、HDR10+ 标签保留，不根据 DV 推断或补充 HDR。Atmos 保留在 `effect` / `edition`，音轨不再重复附加。原始解析数据仍使用标准名称。

例如 `Kingdom.of.Heaven.2005.2160p.Directors.Cut.Roadshow.Version.UHD.BluRay.REMUX.HEVC.DV.HDR.TrueHD.Atmos.7.1-HDHIVE.mkv` 的默认命名为 `Kingdom of Heaven.2005.REMUX.DV.HDR.Atmos.2160p.H.265.TrueHD.7.1-HDHIVE.mkv`。

已有配置和模板不会被覆盖，直接引用这些字段的模板无需添加 `replace`；只使用 `audioCodec` 的自定义模板若要保留效果标签，需要加入 `effect` 或 `edition`。升级后新生成的预览采用新命名；已保存的整理计划仍执行其快照路径，需要重新预览才能使用新格式。下文为 v0.1.1 的初始接入记录。

日期：2026-10-03。后端基线 `51ee358`，前端基线 `dd8734b`，两仓库开始实施时均无未提交改动。此次只交付工作区代码，未提交、推送或部署，也未修改运行中系统的默认引擎。

## 分支与配置

正式依赖为 `github.com/xifofo/medianame v0.1.1`，使用 Go Module 和校验和，没有本机 `replace`。接入的主要代码在 `app/service/medianame_service.go`、`app/handler/organize_medianame.go`、`app/handler/organize_mp2_plan.go` 和 `app/handler/organize_plan.go`。

| 模式 | 识别、分类、命名与目标路径 | 实际整理采用 |
| --- | --- | --- |
| `moviepilot` / MP2 | MoviePilot 识别和转名，原有 FilmFusion 分类与 Jinja 模板、历史路径规则 | MP2 |
| `medianame` | 识别词预处理 → Parse / ParsePath → RecognizeInfo → category → rename → FilmFusion 整理计划 | medianame |
| `shadow` / 影子对比 | 两套完整流程各自使用独立配置，同时记录结果和失败原因 | MP2 |
| `local` | 旧本地引擎兼容入口 | 原有本地流程 |

系统默认值的读取和既有默认 `shadow` 保持不变；已保存的系统值继续有效。单次任务的明确选择优先于目录配置，目录留空时继承系统设置。默认切换只影响后续任务，不触发已有媒体重整理。

旧分类保存于 `media_recognition.categories`，旧 Jinja 模板保存于 `media_recognition.rename_formats`。新配置整体保存于独立键 `media_recognition.medianame.v1`，包含 `category_yaml`、`movie_template`、`tv_template`、`version`。第一次读取时插入 **v0.1.1 包提供的默认规则和模板**，已有配置不会因包升级被覆盖。识别词继续共用。

medianame 模板由包的 Go `text/template` 渲染，MP2 模板继续由原 Jinja 渲染器处理，分别校验和预览，不转换模板语法。新模板的命名变量由包生成；在需要时补充 `episode_title`、`episode_date`、`season_year` 和 `total_episodes`。单集标题/日期模板遇到多集文件会明确报错。默认模板不能表示的跨季资源会保留失败原因，可通过使用完整季集变量的自定义模板处理。

新分支的路径拼接责任固定为：

```text
媒体库根目录 / 分类目录（ClassifyByCategory 控制） / medianame 模板输出
```

模板负责媒体目录和季目录；分类目录由 FilmFusion 统一拼接，模板不提供 `category` 变量。手动分类覆盖依然生效；关闭按分类建目录只去掉路径中的分类层，不修改识别分类。新路径不会再经过旧 Jinja 模板或旧季目录生成器。

## 识别证据与客户端

标题调用 `medianame.Parse`，文件路径调用 `medianame.ParsePath`，在线识别调用 `client.RecognizeInfo` 复用上述解析结果。路径中的目录继承、特殊季 0、多集、季集范围和警告都保存在完整 `meta_info` 中。

只有 `compatible` 且包含媒体详情的结果可以进入自动命名和整理。`review`、`ambiguous`、`not_identified`、`request_error` 均保留状态、原始解析、候选、未解决/已解决冲突及 `selection_basis`。输入中的 ID 不会直接成为已验证的 TMDB 身份，旧评分和标题变体重试也不会替新引擎挑选候选。自动化适配保留原输入中的 ID 信息。

显式 TMDB ID 查询、IMDb/TVDB 外部映射、映射为空后的文件名搜索及请求错误区分均使用包内部实现。网络/API 错误不会作为“没有映射”进入搜索回退。

客户端复用 FilmFusion 的 TMDB 启用状态、地址、API Key / Access Token、超时和缓存时长。地址可带或不带末尾 `/3`。同一配置复用包客户端及有界缓存；配置变化创建新客户端，在途任务继续持有原客户端。补充查询也使用固定的配置和缓存。计划快照不存 TMDB 凭据或 115 Cookie。

## 配置、对比与试用入口

| 入口 | 使用方式 |
| --- | --- |
| 媒体识别页面 | “medianame · 独立配置与完整流程对比”编辑新 YAML、电影/剧集 Go 模板，查看变量示例并单独保存；原 MP2 分类和 Jinja 编辑区继续保留 |
| 同页“预览完整流程” | 选择文件路径/发布标题以及 MP2、medianame、影子或系统默认；设置根目录、分类覆盖和目录开关；使用新配置草稿对比完整路径，不访问 115 |
| 目录整理页 → 加入预整理 | 单次选择预览引擎；分类选项随所选引擎更新；展开结果查看两套身份、季集、分类、目标名、完整路径与诊断 |
| 目录整理页 → 核对整理结果 | 影子预览默认展示 medianame 的识别、命名与完整路径，可切换查看 MP2；显示切换不改变勾选文件、已保存计划或实际执行引擎 |
| 目录整理页 → 确认整理 | 在“本次实际整理引擎”选择 MP2 或 medianame；切换时重新预览已选文件，确认框显示新目标路径；最后点击“使用所选引擎执行整理” |
| 目录配置 → 创建/编辑 | 设置该目录的整理引擎，留空继承系统默认，用于小范围试用 |
| 自动化编辑 → 媒体识别 | 选择 medianame / MP2 / 影子，生成的识别节点和整理节点使用同一模式；“仅生成整理预览”可保存计划和对比而不执行文件操作 |
| 系统设置 → 媒体识别方式 | 完成影子观察和小范围试用后，用户自行切换默认；可随时将后续任务切回 MP2 |

自动化整理继承上游节点已经固定的引擎；明确配置与上游冲突时拒绝执行。没有上游引擎时，依次使用整理节点、目录、系统配置。每批整理先保存完整预览计划，再执行这份计划；预览模式输出 `plans`、`items`、`planned_count` 和 `planned_strm_count`，实际 `organized_count`、`strm_count` 为 0。新分支失败的完整证据也保留在预览输出中。

影子对比包含媒体类型、TMDB ID、标题、年份、完整季集、分类、目标文件名、完整路径、识别或命名失败原因。失败的新分支不影响成功的 MP2 主计划。

识别测试页的卡片和对比表优先展示 medianame，完整流程测试初始选择 medianame。整理页的影子预览同样默认先看 medianame，失败或需要复核时直接显示诊断；缺少新引擎结果的历史条目提示重新预览，不会拿 MP2 字段补成 medianame 结果。目录检查和版本分组标明其来自 MP2 计划，真实执行结果按实际采用的引擎展示。以上只影响查看方式，不保存或切换系统、目录默认配置。

影子预览打开确认框时默认选中 MP2。用户可以在最终确认时改用 medianame；此操作会创建该引擎的新预览计划，只涉及已勾选文件，不修改原计划或系统/目录默认配置。路径加载期间或新计划含识别/命名错误时禁止执行。切回原引擎可继续使用原计划，迟到的另一引擎预览响应不会覆盖当前选择。

## 预览与确认执行

迁移新增 `organize_plans` 表，并给 `cloud_directories` 增加可为空的 `recognition_source`。预览开始时固定模式、识别词、两套分类与模板、包版本和配置哈希；保存服务器拥有的最终文件计划、STRM 路径/内容、源目录清单和目标目录条件。

`POST /api/organize/115-cookie` 的首次调用应为 `dry_run: true`。响应包含 `plan_id` 和 `pipeline`。确认执行提交该 `plan_id`、原整理选项及可选文件子集，`dry_run: false`。客户端不能提交新目标路径替换已保存计划。

- 默认引擎、识别词或模板之后改变，已确认计划仍按保存的引擎及最终输出执行，不重新调用识别或命名。
- 目录根路径、分类开关、账号关联等目录条件改变，或源目录清单、文件名、大小、PickCode 改变，执行前要求重新预览。
- 引擎、整理选项或所选范围与原计划不符，文件不在计划内，或多个文件指向同一目标时拒绝执行。
- 无 `plan_id` / 配置快照的历史预览必须重新预览；不会猜测历史引擎。前端显示对应提示。
- 计划状态通过原子更新从 `ready` 进入 `executing`，完成后为 `executed` 或 `failed`。已开始执行的计划不能重复使用；失败后先核对结果，再重新预览。

文件移动、115 重命名、字幕下载和 STRM 写入仍由 FilmFusion 的执行服务负责。本次验证只使用模拟 115 客户端；模拟确认执行写入的 STRM 均在测试临时目录。

## API 补充

以下路径均要求原有登录认证：

| 方法和路径 | 用途 |
| --- | --- |
| `GET/PUT /api/media-recognition/medianame-config` | 读取/保存新引擎独立配置 |
| `POST /api/media-recognition/medianame-config/validate` | 校验分类和两套 Go 模板并返回示例路径 |
| `POST /api/media-recognition/medianame-config/preview` | 单个模板及命名上下文预览 |
| `POST /api/media-recognition/pipeline-test` | 同整理入口的完整流程，只生成路径和诊断 |
| 原整理分类接口的 `recognition_source` 查询参数 | 返回对应引擎的分类选项 |

完整流程请求示例：

```json
{
  "input": "/Example.2024.[tmdb=123]/Season 0/01.mkv",
  "mode": "file",
  "source": "shadow",
  "root": "影视",
  "classify": true,
  "category": ""
}
```

可选 `medianame_config` 为与保存接口相同结构的临时草稿，不写入已保存配置。

## 验证记录

实施前基线：后端 `go test ./...`、`go build ./...` 通过；前端 35 个测试文件 / 129 项测试、`pnpm tsc`、`pnpm build` 通过。构建已有大于 500 kB 的资源分块提示。

实施后验证包括：

- 后端全量测试、构建、`go vet`；新引擎与整理计划相关测试的 race 检查。
- `go mod verify`，并核实 `go list -m -json github.com/xifofo/medianame` 返回 v0.1.1 且没有 Replace。
- 新旧配置隔离、包默认值、显式 ID、外部映射为空和请求失败、歧义/冲突、目录继承、特殊季与多集、跨季自定义模板、Go/Jinja 分别校验、单集补充字段、根目录/分类/季目录组合。
- 客户端缓存复用和配置变化，完整影子路径，MP2 执行分支，默认/模板变化后的计划回放，源文件/目录/选项变化、越权与历史预览拒绝，防止同一计划重复执行。
- 自动化引擎传递、冲突拒绝、原始 ID 信息保留、只预览的完整输出；标题和文件路径工作台调用同一整理计划构建器。
- 确认框双向切换 MP2 / medianame、所选文件和目录上下文保留、失败时阻止执行、快速切换时忽略迟到响应，以及执行所选引擎的新计划且保留原影子预览。
- 展示优先级补充：修改前相关 3 个测试文件 / 17 项测试通过；补充覆盖 medianame 默认展示、对比列顺序、失败与缺失结果不借用 MP2 字段，以及切换查看后确认仍使用原计划和明确选择的实际引擎。
- 前端 37 个测试文件 / 143 项测试、类型检查和生产构建；本次修改文件的 Biome 与 Ant Design 检查。

整理集成测试的实际预览输出（模拟媒体详情及两套独立分类配置）：

```text
输入：Example.2024.S00E01.[tmdb=123].mkv
MP2：/影视/MP2剧集/OLD/Example/S00E01.mkv
medianame：/影视/NEW剧集/Example (2024) {tmdb-123}/Season 00/Example.2024.S00E01.mkv
执行分支：MP2
```

未验证：真实 TMDB / MoviePilot 服务在用户网络环境下的返回、用户全部媒体样本的识别质量、浏览器登录后的真实账号操作、真实 115 写操作、生产数据库迁移与部署。影子观察仍需要在实际数据上进行，当前测试通过不代表已经达到切换生产默认的条件。计划记录目前保留在数据库中，长期大量预览时需关注表容量。
