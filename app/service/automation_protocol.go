package service

// AutomationVariableProtocol is the single contract used by the workflow
// editor to describe a node input or output. New node types are not considered
// supported until they are registered in automationNodeProtocols.
type AutomationVariableProtocol struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Example     any    `json:"example,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Template    bool   `json:"template,omitempty"`
}

type AutomationNodeProtocol struct {
	Type    string                       `json:"type"`
	Label   string                       `json:"label"`
	Inputs  []AutomationVariableProtocol `json:"inputs"`
	Outputs []AutomationVariableProtocol `json:"outputs"`
}

func automationVariable(name, valueType, label, description string, example any) AutomationVariableProtocol {
	return AutomationVariableProtocol{
		Name: name, Type: valueType, Label: label, Description: description, Example: example,
	}
}

func automationTemplateVariable(name, valueType, label, description string, example any, required bool) AutomationVariableProtocol {
	field := automationVariable(name, valueType, label, description, example)
	field.Required = required
	field.Template = true
	return field
}

var automationNodeProtocols = []AutomationNodeProtocol{
	{Type: AutomationNodeTrigger, Label: "触发事件", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("selected_port", "string", "流程出口", "固定为 next。触发事件字段均可通过 $item.* 引用。", "next"),
		automationVariable("title", "string", "事件名称", "RSS 条目标题，或 115 新增文件和目录名称。", "Example.Show.S01"),
		automationVariable("download_url", "string", "下载链接", "RSS 条目解析出的种子、磁力或下载链接。", "magnet:?xt=urn:btih:example"),
		automationVariable("detail_url", "string", "详情链接", "RSS 条目的详情页面链接。", "https://example.com/topic/1"),
		automationVariable("path", "string", "事件路径", "115 监控目录下的完整路径。", "/入库/Example.Show.S01"),
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "触发事件所在的 115 账号。", 1),
		automationVariable("access_method", "string", "访问方式", "目录监控固定使用 OpenAPI。", "openapi"),
		automationVariable("directory_id", "string", "父目录 ID", "新增对象的父目录 ID。", "0"),
		automationVariable("file_ids", "array", "对象 ID", "可直接传给识别和整理节点的 115 对象 ID。", []string{"123456"}),
		automationVariable("file_names", "array", "对象名称", "与 file_ids 对应的文件或目录名称。", []string{"Example.Show.S01"}),
		automationVariable("tasks", "array", "115 媒体批次", "兼容识别与整理节点的已完成媒体批次。", []map[string]any{{"status": 2, "file_id": "123456"}}),
		automationVariable("completed", "boolean", "上传已完成", "仅在对象稳定并完成上传后触发。", true),
	}},
	{Type: AutomationNodeKeyword, Label: "关键词判断", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "string", "输入字段", "要检查的 事件字段或上游变量。", "$item.title", true),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("matched", "boolean", "是否匹配", "关键词规则是否满足。", true),
		automationVariable("matched_keywords", "array", "命中关键词", "本次命中的关键词列表。", []string{"2160p"}),
		automationVariable("match_mode", "string", "匹配规则", "实际使用的关键词匹配规则。", "contains_any"),
	}},
	{Type: AutomationNodeKeywordReplace, Label: "关键词替换", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "string", "输入字段", "要按顺序执行关键词替换的 事件字段或上游变量。", "$item.title", true),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("result", "string", "替换结果", "完成全部关键词规则后的文本。", "示例剧 S01E01 2160p"),
		automationVariable("replacement_count", "integer", "替换次数", "全部关键词规则实际完成的替换次数。", 2),
		automationVariable("variables", "object", "生成变量", "替换结果写入 $vars.* 的变量对象。", map[string]any{"normalized_title": "示例剧 S01E01 2160p"}),
	}},
	{Type: AutomationNodeRegex, Label: "正则提取", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "string", "输入字段", "要执行正则提取的 事件字段或上游变量。", "$item.title", true),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("matched", "boolean", "是否匹配", "正则表达式是否命中。", true),
		automationVariable("captured", "string", "捕获内容", "指定捕获组的原始文本。", "12"),
		automationVariable("variables", "object", "生成变量", "写入 $vars.* 的变量对象。", map[string]any{"episode": 12}),
	}},
	{Type: AutomationNodeRegexReplace, Label: "正则替换", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "string", "输入字段", "要执行正则全局替换的 事件字段或上游变量。", "$item.title", true),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("result", "string", "替换结果", "使用 Go/RE2 捕获组替换后的文本。", "示例剧 S01E01"),
		automationVariable("replacement_count", "integer", "替换次数", "正则表达式实际命中的次数。", 1),
		automationVariable("variables", "object", "生成变量", "替换结果写入 $vars.* 的变量对象。", map[string]any{"clean_title": "示例剧 S01E01"}),
	}},
	{Type: AutomationNodeConvert, Label: "类型转换", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "any", "输入字段", "要转换的 事件字段或上游变量。", "$vars.episode", true),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("variables", "object", "生成变量", "转换后写入 $vars.* 的变量对象。", map[string]any{"episode": 12}),
	}},
	{Type: AutomationNodeIf, Label: "IF 判断", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("condition.field", "any", "比较字段", "参与条件判断的 事件字段、变量或上游输出。", "$vars.episode", true),
		automationTemplateVariable("condition.value", "any", "比较值", "固定值或另一个流程变量。", 10, false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("matched", "boolean", "判断结果", "IF 条件是否成立。", true),
	}},
	{Type: AutomationNodeParallel, Label: "并行分支", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("selected_ports", "array", "激活分支", "本次激活的并行出口。", []string{"branch-download", "branch-notify"}),
	}},
	{Type: AutomationNodeJoin, Label: "汇合", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("policy", "string", "汇合策略", "实际使用的汇合策略。", "all_completed"),
		automationVariable("active_inputs", "integer", "激活输入数", "进入汇合节点的有效分支数量。", 2),
		automationVariable("successful_inputs", "integer", "成功输入数", "成功完成的有效分支数量。", 2),
	}},
	{Type: AutomationNodeQBittorrent, Label: "qBittorrent 下载", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "下载地址", "磁力链接或 HTTP/HTTPS 种子地址。", "$item.download_url", true),
		automationTemplateVariable("save_path", "string", "保存路径", "qBittorrent 保存路径，支持模板变量。", "/downloads/{{item.category}}", false),
		automationTemplateVariable("category", "string", "分类", "qBittorrent 分类，支持模板变量。", "{{item.category}}", false),
		automationTemplateVariable("tags", "string", "标签", "逗号分隔的 qBittorrent 标签，支持模板变量。", "automation,{{nodes.mp.output.media_type}}", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("target_id", "integer", "下载器 ID", "所使用的下载器账号 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "所使用的下载器账号名称。", "家庭 NAS qB"),
		automationVariable("content_key", "string", "内容键", "下载地址计算出的稳定去重键。", "e3b0c442..."),
		automationVariable("torrent_tag", "string", "跟踪标签", "等待节点用于定位任务的内部标签。", "filmfusion-automation-e3b0c442"),
		automationVariable("submitted", "boolean", "已提交", "任务是否已提交给 qBittorrent。", true),
	}},
	{Type: AutomationNodeWaitQBittorrent, Label: "等待 qBittorrent 完成", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("target_id", "integer", "下载器 ID", "任务所在的 qBittorrent 下载器 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "任务所在的 qBittorrent 下载器名称。", "家庭 NAS qB"),
		automationVariable("torrent_tag", "string", "跟踪标签", "FilmFusion 提交任务时附加的内部跟踪标签。", "filmfusion-automation-e3b0c442"),
		automationVariable("completed", "boolean", "是否完成", "qBittorrent 下载是否完成。", true),
		automationVariable("progress", "number", "下载进度", "0 到 100 的下载进度。", 100),
		automationVariable("state", "string", "任务状态", "qBittorrent 返回的任务状态。", "uploading"),
		automationVariable("hash", "string", "Torrent Hash", "qBittorrent 任务哈希。", "0123456789abcdef"),
		automationVariable("name", "string", "任务名称", "qBittorrent 任务名称。", "Example.Movie.2026"),
		automationVariable("save_path", "string", "保存目录", "qBittorrent 保存目录。", "/downloads/movies"),
		automationVariable("content_path", "string", "完成路径", "下载完成后的文件或文件夹路径。", "/downloads/movies/Example.Movie.2026"),
		automationVariable("content_type", "string", "完成路径类型", "下载结果是 file 或 dir。", "dir"),
		automationVariable("file_count", "integer", "任务文件数", "Torrent 中包含的文件数量。", 12),
		automationVariable("size", "integer", "文件大小", "任务总字节数。", 10737418240),
		automationVariable("ratio", "number", "分享率", "当前上传分享率。", 1.25),
	}},
	{Type: AutomationNodeMoviePilotTransfer, Label: "MP2 整理入库", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("source_path", "string", "MP2 可见源路径", "留空使用上游 qB 完成路径；容器挂载不同可显式改写。", "{{nodes.wait_qb.output.content_path}}", false),
		automationTemplateVariable("tmdb_id", "string", "辅助 TMDB ID", "可选；提供后由 MP2 按指定媒体辅助识别整理。", "1396", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("organized", "boolean", "整理成功", "MP2 同步整理调用是否成功完成。", true),
		automationVariable("source_path", "string", "整理源路径", "MP2 实际收到的文件或目录路径。", "/downloads/Example.Movie.2026"),
		automationVariable("content_type", "string", "源路径类型", "整理源是 file 或 dir。", "dir"),
		automationVariable("tmdb_id", "string", "辅助 TMDB ID", "本次整理使用的辅助 TMDB ID。", "1396"),
		automationVariable("media_type", "string", "媒体类型", "本次整理指定的 auto/movie/tv。", "movie"),
		automationVariable("hash", "string", "Torrent Hash", "整理完成后继续传递的 qBittorrent 任务哈希。", "0123456789abcdef"),
		automationVariable("target_id", "integer", "下载器 ID", "整理完成后继续传递的 qBittorrent 下载器 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "整理完成后继续传递的 qBittorrent 下载器名称。", "家庭 NAS qB"),
		automationVariable("message", "string", "MP2 消息", "MoviePilot 返回的整理结果消息。", "整理完成"),
	}},
	{Type: AutomationNodeDeleteQBittorrent, Label: "删除 qB 做种任务", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("deleted", "boolean", "删除成功", "qBittorrent 做种任务是否已删除。", true),
		automationVariable("already_missing", "boolean", "原本已不存在", "执行时任务是否已经不在 qBittorrent 中。", false),
		automationVariable("delete_files", "boolean", "同时删除文件", "是否要求 qBittorrent 一并删除下载数据。", false),
		automationVariable("hash", "string", "Torrent Hash", "被删除的 qBittorrent 任务哈希。", "0123456789abcdef"),
		automationVariable("target_id", "integer", "下载器 ID", "执行删除的 qBittorrent 下载器 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "执行删除的 qBittorrent 下载器名称。", "家庭 NAS qB"),
		automationVariable("deleted_at", "datetime", "删除时间", "实际向 qBittorrent 提交删除的时间。", "2026-08-16T12:00:00Z"),
	}},
	{Type: AutomationNodeOffline115, Label: "115 云下载（Cookie）", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "下载地址", "提交到 115 的下载地址。", "$item.download_url", true),
	}, Outputs: automation115SubmitOutputs()},
	{Type: AutomationNodeOffline115OpenAPI, Label: "115 云下载（OpenAPI）", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "下载地址", "提交到 115 OpenAPI 的下载地址。", "$item.download_url", true),
	}, Outputs: automation115SubmitOutputs()},
	{Type: AutomationNodeWait115, Label: "等待 115 下载完成", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("completed", "boolean", "是否下载完成", "所有 115 云下载任务是否完成。", true),
		automationVariable("percent", "number", "下载进度", "所有任务的平均下载进度。", 100),
		automationVariable("file_id", "string", "首个完成文件 ID", "单任务完成时返回的文件或文件夹 ID。", "123456"),
		automationVariable("file_name", "string", "首个完成文件名", "单任务完成时返回的文件或文件夹名称。", "Example.Movie.2026"),
		automationVariable("file_ids", "array", "完成文件 ID", "全部完成任务的文件或文件夹 ID。", []string{"123456"}),
		automationVariable("file_names", "array", "完成文件名", "全部完成任务的文件或文件夹名称。", []string{"Example.Movie.2026"}),
		automationVariable("directory_id", "string", "下载目录 ID", "115 离线任务保存目录 ID。", "0"),
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行下载的云存储账号 ID。", 1),
		automationVariable("tasks", "array", "完成任务", "115 云下载任务详情。", []any{}),
	}},
	{Type: AutomationNodeRename115OpenAPI, Label: "115 API 重命名", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("file_id", "string", "文件或文件夹 ID", "要通过 115 OpenAPI 重命名的对象 ID，通常引用等待节点的 file_id。", "{{nodes.wait115.output.file_id}}", true),
		automationTemplateVariable("new_name", "string", "新名称", "完整的新文件名或文件夹名；重命名文件时请按需保留扩展名。", "{{item.title}}.mkv", true),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("renamed", "boolean", "重命名成功", "115 OpenAPI 是否已成功完成重命名。", true),
		automationVariable("file_id", "string", "文件或文件夹 ID", "已重命名对象的 115 ID。", "123456"),
		automationVariable("file_name", "string", "实际名称", "115 OpenAPI 返回的实际文件名或文件夹名。", "示例剧.S01E01.mkv"),
		automationVariable("new_name", "string", "请求名称", "本次提交给 115 OpenAPI 的新名称。", "示例剧.S01E01.mkv"),
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行重命名的云存储账号 ID。", 1),
		automationVariable("storage_name", "string", "115 账号名称", "执行重命名的账号名称。", "115 主账号"),
		automationVariable("access_method", "string", "访问方式", "固定为 openapi。", "openapi"),
	}},
	{Type: AutomationNodeMoviePilotTitle, Label: "MP 标题识别", Inputs: automationRecognitionInputs("$item.title"), Outputs: automationRecognitionOutputs()},
	{Type: AutomationNodeFilmFusionRecognize, Label: "FilmFusion 本地识别", Inputs: []AutomationVariableProtocol{
		automationVariable("recognition_mode", "string", "识别模式", "title 识别发布标题；file 递归识别等待节点返回的 115 下载文件。", "title"),
		automationTemplateVariable("input", "string", "待识别标题", "发布标题模式的输入，可引用 事件字段、流程变量或上游输出。", "$item.title", false),
		automationTemplateVariable("tmdb_id", "string", "辅助 TMDB ID", "可选；提供后会在本地识别输入中加入 TMDB 标记并校验识别结果。", "1396", false),
		automationVariable("lookup_tmdb", "boolean", "查询 TMDB", "是否在本地解析后使用 FilmFusion 的 TMDB 配置补全媒体信息。", true),
	}, Outputs: automationLocalRecognitionOutputs()},
	{Type: AutomationNodeMediaExists, Label: "本地 / Emby 查重", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("tmdb_id", "string", "TMDB ID", "用于查找本地目录和 Emby 项目。", "{{nodes.mp_title.output.tmdb_id}}", true),
		automationTemplateVariable("title", "string", "媒体标题", "用于计算目标目录。", "{{nodes.mp_title.output.title}}", false),
		automationTemplateVariable("year", "string", "年份", "用于计算目标目录。", "{{nodes.mp_title.output.year}}", false),
		automationTemplateVariable("media_type", "string", "媒体类型", "电影或电视剧。", "{{nodes.mp_title.output.media_type}}", false),
		automationTemplateVariable("category", "string", "媒体分类", "可选的 MoviePilot 分类。", "{{nodes.mp_title.output.category}}", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("exists", "boolean", "是否已存在", "本地目录或 Emby 中是否已经存在。", true),
		automationVariable("local_exists", "boolean", "本地是否存在", "目标媒体目录是否已存在。", true),
		automationVariable("local_dir", "string", "本地目录", "已存在的本地媒体目录。", "/media/Movies/Example (2026)"),
		automationVariable("target_dir", "string", "预计目标目录", "按目录配置计算出的目标目录。", "电影/Example (2026)"),
		automationVariable("emby_item_id", "string", "Emby 项目 ID", "匹配到的 Emby Item ID。", "12345"),
		automationVariable("emby_url", "string", "Emby 页面", "匹配项目的 Emby Web 地址。", "https://emby.example/web/index.html#!/item?id=12345"),
		automationVariable("existing_seasons", "array", "已有季", "本地已存在的季度目录。", []string{"Season 01"}),
	}},
	{Type: AutomationNodeHDHiveQuery, Label: "HDHive 资源查询", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("tmdb_id", "string", "TMDB ID", "查询 HDHive 资源使用的 TMDB ID。", "{{nodes.mp_title.output.tmdb_id}}", true),
		automationTemplateVariable("media_type", "string", "媒体类型", "查询类型 movie 或 tv。", "{{nodes.mp_title.output.media_type}}", true),
		automationTemplateVariable("resolution", "string", "分辨率筛选", "可选的分辨率关键词。", "2160p", false),
		automationTemplateVariable("pan_type", "string", "网盘类型筛选", "可选的网盘类型。", "115", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("resource_count", "integer", "资源数量", "筛选后可用的资源数量。", 3),
		automationVariable("selected_slug", "string", "首选资源 slug", "自动选择的首个资源标识。", "example-resource"),
		automationVariable("selected_title", "string", "首选资源标题", "自动选择的资源标题。", "Example 2160p"),
		automationVariable("selected_size", "string", "首选资源大小", "HDHive 返回的资源大小文本。", "12.5 GB"),
		automationVariable("selected_resolution", "array", "首选分辨率", "首选资源包含的分辨率标签。", []string{"2160p"}),
		automationVariable("is_unlocked", "boolean", "是否已解锁", "首选资源是否已经解锁。", false),
		automationVariable("resources", "array", "资源列表", "筛选后的 HDHive 资源详情。", []any{}),
	}},
	{Type: AutomationNodeHDHiveUnlock, Label: "HDHive 资源解锁", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("slug", "string", "资源 slug", "需要解锁的 HDHive 资源标识。", "{{nodes.hdhive_query.output.selected_slug}}", true),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("download_url", "string", "下载地址", "可直接交给下载节点的完整资源地址。", "https://115.com/s/example?password=abcd"),
		automationVariable("url", "string", "资源地址", "HDHive 返回的原始地址。", "https://115.com/s/example"),
		automationVariable("access_code", "string", "访问码", "资源访问码。", "abcd"),
		automationVariable("already_owned", "boolean", "已拥有", "此前是否已经解锁过该资源。", true),
	}},
	{Type: AutomationNodeMoviePilotRecognize, Label: "MP2 / 影子识别", Inputs: automationRecognitionInputs(""), Outputs: append(automationRecognitionOutputs(),
		automationVariable("requested_tmdb_id", "string", "辅助 TMDB ID", "本次识别实际使用的辅助 TMDB ID；留空自动识别时为空。", "1396"),
		automationVariable("total_files", "integer", "媒体文件数", "115 下载结果中找到的可识别媒体文件数量。", 1),
		automationVariable("recognized_count", "integer", "识别文件数", "成功识别的媒体文件数量。", 1),
		automationVariable("failed_count", "integer", "识别失败数", "未能识别的媒体文件数量。", 0),
		automationVariable("items", "array", "识别结果", "每个媒体文件的识别详情。", []any{}),
		automationVariable("failed_items", "array", "识别失败明细", "未能识别的媒体文件及原因。", []any{}),
		automationVariable("partial", "boolean", "部分识别", "是否只有部分文件识别成功。", false),
	)},
	{Type: AutomationNodeOrganizeStrm, Label: "整理生成 STRM", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("organized_count", "integer", "整理文件数", "成功整理的媒体文件数量。", 1),
		automationVariable("strm_count", "integer", "STRM 数量", "成功生成的 STRM 文件数量。", 1),
		automationVariable("failed_count", "integer", "失败数量", "整理失败的项目数量。", 0),
		automationVariable("target_path", "string", "首个目标路径", "首个媒体文件整理后的云端路径。", "/Movies/Example (2026)/Example.mkv"),
		automationVariable("strm_path", "string", "首个 STRM 路径", "首个生成的本地 STRM 文件路径。", "/media/Movies/Example (2026)/Example.strm"),
		automationVariable("strm_content", "string", "首个 STRM 内容", "首个 STRM 文件写入的内容。", "https://example/115/example.mkv"),
		automationVariable("cloud_directory_name", "string", "目录配置名称", "所使用的目录配置。", "影视中心"),
		automationVariable("source_folder_ids", "array", "来源文件夹 ID", "本次整理使用的来源文件夹 ID。", []string{"123456"}),
		automationVariable("source_folder_deleted", "boolean", "源目录已删除", "来源文件夹是否已经删除。", false),
		automationVariable("source_folder_delete_pending", "boolean", "源目录等待删除", "是否等待字幕下载后再删除来源目录。", true),
		automationVariable("items", "array", "整理明细", "每个媒体文件和字幕的整理结果。", []any{}),
	}},
	{Type: AutomationNodeStrmVerify, Label: "STRM 校验", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("valid", "boolean", "是否有效", "全部 STRM 是否存在且内容有效。", true),
		automationVariable("checked_count", "integer", "校验数量", "实际校验的 STRM 文件数量。", 1),
		automationVariable("valid_count", "integer", "有效数量", "通过校验的 STRM 文件数量。", 1),
		automationVariable("invalid_count", "integer", "无效数量", "未通过校验的 STRM 文件数量。", 0),
		automationVariable("strm_path", "string", "首个 STRM 路径", "首个被校验的 STRM 文件路径。", "/media/Movies/Example.strm"),
		automationVariable("strm_content", "string", "首个 STRM 内容", "首个有效 STRM 的内容。", "https://example/115/example.mkv"),
		automationVariable("errors", "array", "校验错误", "无效路径及其原因。", []string{}),
	}},
	{Type: AutomationNodeStrmRegenerate, Label: "STRM 重生成", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("regenerated_count", "integer", "重生成数量", "成功重写的 STRM 文件数量。", 1),
		automationVariable("failed_count", "integer", "失败数量", "未能重新生成的 STRM 数量。", 0),
		automationVariable("strm_path", "string", "首个 STRM 路径", "首个重新生成的 STRM 文件路径。", "/media/Movies/Example.strm"),
		automationVariable("strm_paths", "array", "STRM 路径", "全部成功重生成的 STRM 路径。", []string{"/media/Movies/Example.strm"}),
		automationVariable("errors", "array", "重生成错误", "未能重写的路径及原因。", []string{}),
	}},
	{Type: AutomationNodeEmbyRefreshWait, Label: "Emby 刷新并等待入库", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("tmdb_id", "string", "TMDB ID", "等待 Emby 入库使用的 TMDB ID。", "{{nodes.mp.output.tmdb_id}}", true),
		automationTemplateVariable("media_type", "string", "媒体类型", "限定查询 Movie 或 Series。", "{{nodes.mp.output.media_type}}", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("found", "boolean", "是否已入库", "Emby 是否已经找到对应项目。", true),
		automationVariable("emby_item_id", "string", "Emby 项目 ID", "匹配到的 Emby Item ID。", "12345"),
		automationVariable("emby_url", "string", "Emby 页面", "匹配项目的 Emby Web 地址。", "https://emby.example/web/index.html#!/item?id=12345"),
		automationVariable("refresh_requested", "boolean", "已请求刷新", "本节点是否已经触发过媒体库刷新。", true),
		automationVariable("waiting_seconds", "integer", "等待秒数", "从首次检查到当前的等待时长。", 45),
	}},
	{Type: AutomationNodeHTTPRequest, Label: "HTTP / Webhook", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "请求地址", "HTTP/HTTPS 接口地址，支持流程变量。", "https://hooks.example/api/media/{{nodes.mp.output.tmdb_id}}", true),
		automationTemplateVariable("headers", "object", "请求头", "JSON 对象中的每个值都支持流程变量。", map[string]any{"X-Media-Type": "{{nodes.mp.output.media_type}}"}, false),
		automationTemplateVariable("body", "string", "请求体", "请求体文本，可插入 事件字段和上游节点输出。", "{\"tmdb_id\":\"{{nodes.mp.output.tmdb_id}}\"}", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("status_code", "integer", "HTTP 状态码", "接口返回的 HTTP 状态码。", 200),
		automationVariable("content_type", "string", "内容类型", "响应的 Content-Type。", "application/json"),
		automationVariable("body", "string", "响应正文", "最多 1 MiB 的响应文本。", "{\"ok\":true}"),
		automationVariable("json", "any", "JSON 响应", "响应可解析为 JSON 时返回的结构化内容。", map[string]any{"ok": true}),
		automationVariable("request_host", "string", "请求主机", "实际请求 URL 的主机名，不包含查询参数。", "hooks.example"),
		automationVariable("duration_ms", "integer", "耗时", "HTTP 请求耗时，单位毫秒。", 128),
	}},
	{Type: AutomationNodeNotification, Label: "发送通知", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("title", "string", "通知标题", "通知标题模板。", "{{nodes.mp.output.title}} 已入库", false),
		automationTemplateVariable("message", "string", "通知内容", "通知正文模板。", "STRM：{{nodes.organize.output.strm_path}}", true),
		automationTemplateVariable("image_url", "string", "通知图片", "通知图片或海报地址。", "{{nodes.mp.output.poster_url}}", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("skipped", "boolean", "是否跳过", "通知服务是否跳过了本次发送。", false),
		automationVariable("deliveries", "array", "发送结果", "各通知渠道的发送结果。", []any{}),
		automationVariable("partial", "boolean", "部分失败", "是否只有部分通知渠道发送成功。", false),
	}},
	{Type: AutomationNodeEnd, Label: "结束", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("completed", "boolean", "流程已结束", "结束节点已经执行。", true),
	}},
}

func automation115SubmitOutputs() []AutomationVariableProtocol {
	return []AutomationVariableProtocol{
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行离线下载的云存储账号 ID。", 1),
		automationVariable("storage_name", "string", "115 账号名称", "执行离线下载的账号名称。", "115 主账号"),
		automationVariable("directory_id", "string", "保存目录 ID", "115 离线任务保存目录 ID。", "0"),
		automationVariable("hashes", "array", "任务 Hash", "115 返回的离线任务 Hash。", []string{"abcdef"}),
		automationVariable("access_method", "string", "访问方式", "cookie 或 openapi。", "openapi"),
		automationVariable("content_key", "string", "内容键", "下载地址计算出的稳定去重键。", "e3b0c442..."),
		automationVariable("submitted", "boolean", "已提交", "任务是否已提交给 115。", true),
	}
}

func automationRecognitionInputs(defaultInput string) []AutomationVariableProtocol {
	fields := make([]AutomationVariableProtocol, 0, 3)
	if defaultInput != "" {
		fields = append(fields, automationTemplateVariable("input", "string", "待识别标题", "MoviePilot 标题识别输入。", defaultInput, true))
	}
	fields = append(fields, automationVariable("recognition_source", "string", "识别方式", "moviepilot、local 或 shadow；留空跟随系统设置。", "shadow"))
	fields = append(fields, automationTemplateVariable("tmdb_id", "string", "辅助 TMDB ID", "留空自动识别，或提供 TMDB ID 辅助识别。", "1396", false))
	return fields
}

func automationRecognitionOutputs() []AutomationVariableProtocol {
	return []AutomationVariableProtocol{
		automationVariable("tmdb_id", "string", "TMDB ID", "MoviePilot 识别出的 TMDB ID。", "1396"),
		automationVariable("title", "string", "媒体标题", "识别出的规范媒体标题。", "绝命毒师"),
		automationVariable("year", "string", "年份", "媒体首播或上映年份。", "2008"),
		automationVariable("media_type", "string", "媒体类型", "movie 或 tv。", "tv"),
		automationVariable("season_episode", "string", "季集信息", "识别出的季集信息。", "S01E01"),
		automationVariable("category", "string", "媒体分类", "MoviePilot 分类结果。", "欧美剧"),
		automationVariable("rating", "number", "评分", "识别结果中的评分。", 9.5),
		automationVariable("quality", "string", "质量", "识别出的资源质量。", "2160p"),
		automationVariable("poster_url", "string", "海报地址", "TMDB 海报或背景图地址。", "https://image.tmdb.org/example.jpg"),
	}
}

func automationLocalRecognitionOutputs() []AutomationVariableProtocol {
	return []AutomationVariableProtocol{
		automationVariable("engine", "string", "识别引擎", "固定为 local，表示本次没有调用 MoviePilot。", "local"),
		automationVariable("mode", "string", "实际模式", "本次执行使用的 title 或 file 模式。", "title"),
		automationVariable("recognize_input", "string", "实际识别输入", "应用辅助 TMDB 标记后送入本地识别内核的文本。", "示例剧.S01E01.{tmdb-1396}.mkv"),
		automationVariable("processed_input", "string", "词表处理结果", "按已保存识别词处理后的标题或路径。", "示例剧.S01E01.2160p.mkv"),
		automationVariable("requested_tmdb_id", "string", "辅助 TMDB ID", "本次配置并解析出的辅助 TMDB ID；未配置时为空。", "1396"),
		automationVariable("tmdb_id", "string", "TMDB ID", "本地标记解析或 TMDB 匹配得到的 TMDB ID。", "1396"),
		automationVariable("title", "string", "媒体标题", "识别词处理与本地解析得到的媒体标题。", "绝命毒师"),
		automationVariable("year", "string", "年份", "本地解析或 TMDB 匹配得到的年份。", "2008"),
		automationVariable("media_type", "string", "媒体类型", "movie、tv 或 unknown。", "tv"),
		automationVariable("season_episode", "string", "季集信息", "本地解析得到的季集信息。", "S01E01"),
		automationVariable("category", "string", "媒体分类", "TMDB 匹配补全的媒体分类；纯本地解析时可为空。", "欧美剧"),
		automationVariable("rating", "number", "评分", "TMDB 匹配补全的评分；纯本地解析时为 0。", 9.5),
		automationVariable("quality", "string", "质量", "本地解析得到的分辨率或资源质量。", "2160p"),
		automationVariable("poster_url", "string", "海报地址", "TMDB 匹配得到的海报或背景图地址。", "https://image.tmdb.org/t/p/w780/example.jpg"),
		automationVariable("tmdb_status", "string", "TMDB 状态", "matched、not_found、not_configured、failed 或 skipped。", "matched"),
		automationVariable("warning", "string", "识别提示", "TMDB 不可用等非阻断提示。", "TMDB 未配置，已返回纯本地解析结果"),
		automationVariable("applied_words", "array", "命中识别词", "本次实际命中的 FilmFusion 本地识别词。", []string{"广告 => "}),
		automationVariable("meta_info", "object", "本地解析详情", "本地媒体元数据解析结果。", map[string]any{"name": "绝命毒师", "media_type": "tv"}),
		automationVariable("media_info", "object", "媒体信息", "本地解析与可选 TMDB 匹配后的统一媒体实体。", map[string]any{"title": "绝命毒师", "tmdb_id": "1396"}),
		automationVariable("word_result", "object", "识别词轨迹", "识别词处理前后文本和逐条执行轨迹。", map[string]any{"processed": "绝命毒师.S01E01"}),
		automationVariable("candidates", "array", "TMDB 候选", "本地 TMDB 匹配时参与选择的候选列表。", []any{}),
		automationVariable("raw", "object", "原始结果", "本地识别内核返回的兼容原始结构。", map[string]any{"engine": "local"}),
		automationVariable("total_files", "integer", "媒体文件数", "文件模式在 115 下载结果中找到的视频文件数量。", 1),
		automationVariable("recognized_count", "integer", "识别文件数", "文件模式成功完成本地解析的视频文件数量。", 1),
		automationVariable("failed_count", "integer", "识别失败数", "文件模式未能完成本地解析的视频文件数量。", 0),
		automationVariable("items", "array", "识别结果", "文件模式下每个视频文件的本地识别详情。", []any{}),
		automationVariable("failed_items", "array", "识别失败明细", "文件模式下未能识别的文件及原因。", []any{}),
		automationVariable("partial", "boolean", "部分识别", "文件模式是否只有部分文件识别成功。", false),
	}
}

func AutomationNodeProtocols() []AutomationNodeProtocol {
	return append([]AutomationNodeProtocol(nil), automationNodeProtocols...)
}

func automationNodeProtocolByType(nodeType string) (AutomationNodeProtocol, bool) {
	for _, protocol := range automationNodeProtocols {
		if protocol.Type == nodeType {
			return protocol, true
		}
	}
	return AutomationNodeProtocol{}, false
}
