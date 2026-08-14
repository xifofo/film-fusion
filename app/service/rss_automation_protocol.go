package service

// RSSAutomationVariableProtocol is the single contract used by the workflow
// editor to describe a node input or output. New node types are not considered
// supported until they are registered in rssAutomationNodeProtocols.
type RSSAutomationVariableProtocol struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Example     any    `json:"example,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Template    bool   `json:"template,omitempty"`
}

type RSSAutomationNodeProtocol struct {
	Type    string                          `json:"type"`
	Label   string                          `json:"label"`
	Inputs  []RSSAutomationVariableProtocol `json:"inputs"`
	Outputs []RSSAutomationVariableProtocol `json:"outputs"`
}

func rssAutomationVariable(name, valueType, label, description string, example any) RSSAutomationVariableProtocol {
	return RSSAutomationVariableProtocol{
		Name: name, Type: valueType, Label: label, Description: description, Example: example,
	}
}

func rssAutomationTemplateVariable(name, valueType, label, description string, example any, required bool) RSSAutomationVariableProtocol {
	field := rssAutomationVariable(name, valueType, label, description, example)
	field.Required = required
	field.Template = true
	return field
}

var rssAutomationNodeProtocols = []RSSAutomationNodeProtocol{
	{Type: RSSAutomationNodeTrigger, Label: "RSS 条目进入", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("selected_port", "string", "流程出口", "固定为 next。RSS 原始字段通过 $item.* 引用。", "next"),
	}},
	{Type: RSSAutomationNodeKeyword, Label: "关键词判断", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("input", "string", "输入字段", "要检查的 RSS 字段或上游变量。", "$item.title", true),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("matched", "boolean", "是否匹配", "关键词规则是否满足。", true),
		rssAutomationVariable("matched_keywords", "array", "命中关键词", "本次命中的关键词列表。", []string{"2160p"}),
		rssAutomationVariable("match_mode", "string", "匹配规则", "实际使用的关键词匹配规则。", "contains_any"),
	}},
	{Type: RSSAutomationNodeRegex, Label: "正则提取", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("input", "string", "输入字段", "要执行正则提取的 RSS 字段或上游变量。", "$item.title", true),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("matched", "boolean", "是否匹配", "正则表达式是否命中。", true),
		rssAutomationVariable("captured", "string", "捕获内容", "指定捕获组的原始文本。", "12"),
		rssAutomationVariable("variables", "object", "生成变量", "写入 $vars.* 的变量对象。", map[string]any{"episode": 12}),
	}},
	{Type: RSSAutomationNodeConvert, Label: "类型转换", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("input", "any", "输入字段", "要转换的 RSS 字段或上游变量。", "$vars.episode", true),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("variables", "object", "生成变量", "转换后写入 $vars.* 的变量对象。", map[string]any{"episode": 12}),
	}},
	{Type: RSSAutomationNodeIf, Label: "IF 判断", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("condition.field", "any", "比较字段", "参与条件判断的 RSS 字段、变量或上游输出。", "$vars.episode", true),
		rssAutomationTemplateVariable("condition.value", "any", "比较值", "固定值或另一个流程变量。", 10, false),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("matched", "boolean", "判断结果", "IF 条件是否成立。", true),
	}},
	{Type: RSSAutomationNodeParallel, Label: "并行分支", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("selected_ports", "array", "激活分支", "本次激活的并行出口。", []string{"branch-download", "branch-notify"}),
	}},
	{Type: RSSAutomationNodeJoin, Label: "汇合", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("policy", "string", "汇合策略", "实际使用的汇合策略。", "all_completed"),
		rssAutomationVariable("active_inputs", "integer", "激活输入数", "进入汇合节点的有效分支数量。", 2),
		rssAutomationVariable("successful_inputs", "integer", "成功输入数", "成功完成的有效分支数量。", 2),
	}},
	{Type: RSSAutomationNodeQBittorrent, Label: "qBittorrent 下载", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("url", "string", "下载地址", "磁力链接或 HTTP/HTTPS 种子地址。", "$item.download_url", true),
		rssAutomationTemplateVariable("save_path", "string", "保存路径", "qBittorrent 保存路径，支持模板变量。", "/downloads/{{item.category}}", false),
		rssAutomationTemplateVariable("category", "string", "分类", "qBittorrent 分类，支持模板变量。", "{{item.category}}", false),
		rssAutomationTemplateVariable("tags", "string", "标签", "逗号分隔的 qBittorrent 标签，支持模板变量。", "rss,{{nodes.mp.output.media_type}}", false),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("target_id", "integer", "下载器 ID", "所使用的下载器账号 ID。", 1),
		rssAutomationVariable("target_name", "string", "下载器名称", "所使用的下载器账号名称。", "家庭 NAS qB"),
		rssAutomationVariable("content_key", "string", "内容键", "下载地址计算出的稳定去重键。", "e3b0c442..."),
		rssAutomationVariable("torrent_tag", "string", "跟踪标签", "等待节点用于定位任务的内部标签。", "filmfusion-rss-e3b0c442"),
		rssAutomationVariable("submitted", "boolean", "已提交", "任务是否已提交给 qBittorrent。", true),
	}},
	{Type: RSSAutomationNodeWaitQBittorrent, Label: "等待 qBittorrent 完成", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("completed", "boolean", "是否完成", "qBittorrent 下载是否完成。", true),
		rssAutomationVariable("progress", "number", "下载进度", "0 到 100 的下载进度。", 100),
		rssAutomationVariable("state", "string", "任务状态", "qBittorrent 返回的任务状态。", "uploading"),
		rssAutomationVariable("hash", "string", "Torrent Hash", "qBittorrent 任务哈希。", "0123456789abcdef"),
		rssAutomationVariable("name", "string", "任务名称", "qBittorrent 任务名称。", "Example.Movie.2026"),
		rssAutomationVariable("save_path", "string", "保存目录", "qBittorrent 保存目录。", "/downloads/movies"),
		rssAutomationVariable("content_path", "string", "完成路径", "下载完成后的文件或文件夹路径。", "/downloads/movies/Example.Movie.2026"),
		rssAutomationVariable("size", "integer", "文件大小", "任务总字节数。", 10737418240),
		rssAutomationVariable("ratio", "number", "分享率", "当前上传分享率。", 1.25),
	}},
	{Type: RSSAutomationNodeOffline115, Label: "115 云下载（Cookie）", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("url", "string", "下载地址", "提交到 115 的下载地址。", "$item.download_url", true),
	}, Outputs: rssAutomation115SubmitOutputs()},
	{Type: RSSAutomationNodeOffline115OpenAPI, Label: "115 云下载（OpenAPI）", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("url", "string", "下载地址", "提交到 115 OpenAPI 的下载地址。", "$item.download_url", true),
	}, Outputs: rssAutomation115SubmitOutputs()},
	{Type: RSSAutomationNodeWait115, Label: "等待 115 下载完成", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("completed", "boolean", "是否下载完成", "所有 115 云下载任务是否完成。", true),
		rssAutomationVariable("percent", "number", "下载进度", "所有任务的平均下载进度。", 100),
		rssAutomationVariable("file_id", "string", "首个完成文件 ID", "单任务完成时返回的文件或文件夹 ID。", "123456"),
		rssAutomationVariable("file_name", "string", "首个完成文件名", "单任务完成时返回的文件或文件夹名称。", "Example.Movie.2026"),
		rssAutomationVariable("file_ids", "array", "完成文件 ID", "全部完成任务的文件或文件夹 ID。", []string{"123456"}),
		rssAutomationVariable("file_names", "array", "完成文件名", "全部完成任务的文件或文件夹名称。", []string{"Example.Movie.2026"}),
		rssAutomationVariable("directory_id", "string", "下载目录 ID", "115 离线任务保存目录 ID。", "0"),
		rssAutomationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行下载的云存储账号 ID。", 1),
		rssAutomationVariable("tasks", "array", "完成任务", "115 云下载任务详情。", []any{}),
	}},
	{Type: RSSAutomationNodeMoviePilotTitle, Label: "MP 标题识别", Inputs: rssAutomationRecognitionInputs("$item.title"), Outputs: rssAutomationRecognitionOutputs()},
	{Type: RSSAutomationNodeMediaExists, Label: "本地 / Emby 查重", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("tmdb_id", "string", "TMDB ID", "用于查找本地目录和 Emby 项目。", "{{nodes.mp_title.output.tmdb_id}}", true),
		rssAutomationTemplateVariable("title", "string", "媒体标题", "用于计算目标目录。", "{{nodes.mp_title.output.title}}", false),
		rssAutomationTemplateVariable("year", "string", "年份", "用于计算目标目录。", "{{nodes.mp_title.output.year}}", false),
		rssAutomationTemplateVariable("media_type", "string", "媒体类型", "电影或电视剧。", "{{nodes.mp_title.output.media_type}}", false),
		rssAutomationTemplateVariable("category", "string", "媒体分类", "可选的 MoviePilot 分类。", "{{nodes.mp_title.output.category}}", false),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("exists", "boolean", "是否已存在", "本地目录或 Emby 中是否已经存在。", true),
		rssAutomationVariable("local_exists", "boolean", "本地是否存在", "目标媒体目录是否已存在。", true),
		rssAutomationVariable("local_dir", "string", "本地目录", "已存在的本地媒体目录。", "/media/Movies/Example (2026)"),
		rssAutomationVariable("target_dir", "string", "预计目标目录", "按目录配置计算出的目标目录。", "电影/Example (2026)"),
		rssAutomationVariable("emby_item_id", "string", "Emby 项目 ID", "匹配到的 Emby Item ID。", "12345"),
		rssAutomationVariable("emby_url", "string", "Emby 页面", "匹配项目的 Emby Web 地址。", "https://emby.example/web/index.html#!/item?id=12345"),
		rssAutomationVariable("existing_seasons", "array", "已有季", "本地已存在的季度目录。", []string{"Season 01"}),
	}},
	{Type: RSSAutomationNodeHDHiveQuery, Label: "HDHive 资源查询", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("tmdb_id", "string", "TMDB ID", "查询 HDHive 资源使用的 TMDB ID。", "{{nodes.mp_title.output.tmdb_id}}", true),
		rssAutomationTemplateVariable("media_type", "string", "媒体类型", "查询类型 movie 或 tv。", "{{nodes.mp_title.output.media_type}}", true),
		rssAutomationTemplateVariable("resolution", "string", "分辨率筛选", "可选的分辨率关键词。", "2160p", false),
		rssAutomationTemplateVariable("pan_type", "string", "网盘类型筛选", "可选的网盘类型。", "115", false),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("resource_count", "integer", "资源数量", "筛选后可用的资源数量。", 3),
		rssAutomationVariable("selected_slug", "string", "首选资源 slug", "自动选择的首个资源标识。", "example-resource"),
		rssAutomationVariable("selected_title", "string", "首选资源标题", "自动选择的资源标题。", "Example 2160p"),
		rssAutomationVariable("selected_size", "string", "首选资源大小", "HDHive 返回的资源大小文本。", "12.5 GB"),
		rssAutomationVariable("selected_resolution", "array", "首选分辨率", "首选资源包含的分辨率标签。", []string{"2160p"}),
		rssAutomationVariable("is_unlocked", "boolean", "是否已解锁", "首选资源是否已经解锁。", false),
		rssAutomationVariable("resources", "array", "资源列表", "筛选后的 HDHive 资源详情。", []any{}),
	}},
	{Type: RSSAutomationNodeHDHiveUnlock, Label: "HDHive 资源解锁", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("slug", "string", "资源 slug", "需要解锁的 HDHive 资源标识。", "{{nodes.hdhive_query.output.selected_slug}}", true),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("download_url", "string", "下载地址", "可直接交给下载节点的完整资源地址。", "https://115.com/s/example?password=abcd"),
		rssAutomationVariable("url", "string", "资源地址", "HDHive 返回的原始地址。", "https://115.com/s/example"),
		rssAutomationVariable("access_code", "string", "访问码", "资源访问码。", "abcd"),
		rssAutomationVariable("already_owned", "boolean", "已拥有", "此前是否已经解锁过该资源。", true),
	}},
	{Type: RSSAutomationNodeMoviePilotRecognize, Label: "MP 媒体识别", Inputs: rssAutomationRecognitionInputs(""), Outputs: append(rssAutomationRecognitionOutputs(),
		rssAutomationVariable("requested_tmdb_id", "string", "辅助 TMDB ID", "本次识别实际使用的辅助 TMDB ID；留空自动识别时为空。", "1396"),
		rssAutomationVariable("total_files", "integer", "媒体文件数", "115 下载结果中找到的可识别媒体文件数量。", 1),
		rssAutomationVariable("recognized_count", "integer", "识别文件数", "成功识别的媒体文件数量。", 1),
		rssAutomationVariable("failed_count", "integer", "识别失败数", "未能识别的媒体文件数量。", 0),
		rssAutomationVariable("items", "array", "识别结果", "每个媒体文件的识别详情。", []any{}),
		rssAutomationVariable("failed_items", "array", "识别失败明细", "未能识别的媒体文件及原因。", []any{}),
		rssAutomationVariable("partial", "boolean", "部分识别", "是否只有部分文件识别成功。", false),
	)},
	{Type: RSSAutomationNodeOrganizeStrm, Label: "整理生成 STRM", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("organized_count", "integer", "整理文件数", "成功整理的媒体文件数量。", 1),
		rssAutomationVariable("strm_count", "integer", "STRM 数量", "成功生成的 STRM 文件数量。", 1),
		rssAutomationVariable("failed_count", "integer", "失败数量", "整理失败的项目数量。", 0),
		rssAutomationVariable("target_path", "string", "首个目标路径", "首个媒体文件整理后的云端路径。", "/Movies/Example (2026)/Example.mkv"),
		rssAutomationVariable("strm_path", "string", "首个 STRM 路径", "首个生成的本地 STRM 文件路径。", "/media/Movies/Example (2026)/Example.strm"),
		rssAutomationVariable("strm_content", "string", "首个 STRM 内容", "首个 STRM 文件写入的内容。", "https://example/115/example.mkv"),
		rssAutomationVariable("cloud_directory_name", "string", "目录配置名称", "所使用的目录配置。", "影视中心"),
		rssAutomationVariable("source_folder_ids", "array", "来源文件夹 ID", "本次整理使用的来源文件夹 ID。", []string{"123456"}),
		rssAutomationVariable("source_folder_deleted", "boolean", "源目录已删除", "来源文件夹是否已经删除。", false),
		rssAutomationVariable("source_folder_delete_pending", "boolean", "源目录等待删除", "是否等待字幕下载后再删除来源目录。", true),
		rssAutomationVariable("items", "array", "整理明细", "每个媒体文件和字幕的整理结果。", []any{}),
	}},
	{Type: RSSAutomationNodeStrmVerify, Label: "STRM 校验", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("valid", "boolean", "是否有效", "全部 STRM 是否存在且内容有效。", true),
		rssAutomationVariable("checked_count", "integer", "校验数量", "实际校验的 STRM 文件数量。", 1),
		rssAutomationVariable("valid_count", "integer", "有效数量", "通过校验的 STRM 文件数量。", 1),
		rssAutomationVariable("invalid_count", "integer", "无效数量", "未通过校验的 STRM 文件数量。", 0),
		rssAutomationVariable("strm_path", "string", "首个 STRM 路径", "首个被校验的 STRM 文件路径。", "/media/Movies/Example.strm"),
		rssAutomationVariable("strm_content", "string", "首个 STRM 内容", "首个有效 STRM 的内容。", "https://example/115/example.mkv"),
		rssAutomationVariable("errors", "array", "校验错误", "无效路径及其原因。", []string{}),
	}},
	{Type: RSSAutomationNodeStrmRegenerate, Label: "STRM 重生成", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("regenerated_count", "integer", "重生成数量", "成功重写的 STRM 文件数量。", 1),
		rssAutomationVariable("failed_count", "integer", "失败数量", "未能重新生成的 STRM 数量。", 0),
		rssAutomationVariable("strm_path", "string", "首个 STRM 路径", "首个重新生成的 STRM 文件路径。", "/media/Movies/Example.strm"),
		rssAutomationVariable("strm_paths", "array", "STRM 路径", "全部成功重生成的 STRM 路径。", []string{"/media/Movies/Example.strm"}),
		rssAutomationVariable("errors", "array", "重生成错误", "未能重写的路径及原因。", []string{}),
	}},
	{Type: RSSAutomationNodeEmbyRefreshWait, Label: "Emby 刷新并等待入库", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("tmdb_id", "string", "TMDB ID", "等待 Emby 入库使用的 TMDB ID。", "{{nodes.mp.output.tmdb_id}}", true),
		rssAutomationTemplateVariable("media_type", "string", "媒体类型", "限定查询 Movie 或 Series。", "{{nodes.mp.output.media_type}}", false),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("found", "boolean", "是否已入库", "Emby 是否已经找到对应项目。", true),
		rssAutomationVariable("emby_item_id", "string", "Emby 项目 ID", "匹配到的 Emby Item ID。", "12345"),
		rssAutomationVariable("emby_url", "string", "Emby 页面", "匹配项目的 Emby Web 地址。", "https://emby.example/web/index.html#!/item?id=12345"),
		rssAutomationVariable("refresh_requested", "boolean", "已请求刷新", "本节点是否已经触发过媒体库刷新。", true),
		rssAutomationVariable("waiting_seconds", "integer", "等待秒数", "从首次检查到当前的等待时长。", 45),
	}},
	{Type: RSSAutomationNodeHTTPRequest, Label: "HTTP / Webhook", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("url", "string", "请求地址", "HTTP/HTTPS 接口地址，支持流程变量。", "https://hooks.example/api/media/{{nodes.mp.output.tmdb_id}}", true),
		rssAutomationTemplateVariable("headers", "object", "请求头", "JSON 对象中的每个值都支持流程变量。", map[string]any{"X-Media-Type": "{{nodes.mp.output.media_type}}"}, false),
		rssAutomationTemplateVariable("body", "string", "请求体", "请求体文本，可插入 RSS 字段和上游节点输出。", "{\"tmdb_id\":\"{{nodes.mp.output.tmdb_id}}\"}", false),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("status_code", "integer", "HTTP 状态码", "接口返回的 HTTP 状态码。", 200),
		rssAutomationVariable("content_type", "string", "内容类型", "响应的 Content-Type。", "application/json"),
		rssAutomationVariable("body", "string", "响应正文", "最多 1 MiB 的响应文本。", "{\"ok\":true}"),
		rssAutomationVariable("json", "any", "JSON 响应", "响应可解析为 JSON 时返回的结构化内容。", map[string]any{"ok": true}),
		rssAutomationVariable("request_host", "string", "请求主机", "实际请求 URL 的主机名，不包含查询参数。", "hooks.example"),
		rssAutomationVariable("duration_ms", "integer", "耗时", "HTTP 请求耗时，单位毫秒。", 128),
	}},
	{Type: RSSAutomationNodeNotification, Label: "发送通知", Inputs: []RSSAutomationVariableProtocol{
		rssAutomationTemplateVariable("title", "string", "通知标题", "通知标题模板。", "{{nodes.mp.output.title}} 已入库", false),
		rssAutomationTemplateVariable("message", "string", "通知内容", "通知正文模板。", "STRM：{{nodes.organize.output.strm_path}}", true),
		rssAutomationTemplateVariable("image_url", "string", "通知图片", "通知图片或海报地址。", "{{nodes.mp.output.poster_url}}", false),
	}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("skipped", "boolean", "是否跳过", "通知服务是否跳过了本次发送。", false),
		rssAutomationVariable("deliveries", "array", "发送结果", "各通知渠道的发送结果。", []any{}),
		rssAutomationVariable("partial", "boolean", "部分失败", "是否只有部分通知渠道发送成功。", false),
	}},
	{Type: RSSAutomationNodeEnd, Label: "结束", Inputs: []RSSAutomationVariableProtocol{}, Outputs: []RSSAutomationVariableProtocol{
		rssAutomationVariable("completed", "boolean", "流程已结束", "结束节点已经执行。", true),
	}},
}

func rssAutomation115SubmitOutputs() []RSSAutomationVariableProtocol {
	return []RSSAutomationVariableProtocol{
		rssAutomationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行离线下载的云存储账号 ID。", 1),
		rssAutomationVariable("storage_name", "string", "115 账号名称", "执行离线下载的账号名称。", "115 主账号"),
		rssAutomationVariable("directory_id", "string", "保存目录 ID", "115 离线任务保存目录 ID。", "0"),
		rssAutomationVariable("hashes", "array", "任务 Hash", "115 返回的离线任务 Hash。", []string{"abcdef"}),
		rssAutomationVariable("access_method", "string", "访问方式", "cookie 或 openapi。", "openapi"),
		rssAutomationVariable("content_key", "string", "内容键", "下载地址计算出的稳定去重键。", "e3b0c442..."),
		rssAutomationVariable("submitted", "boolean", "已提交", "任务是否已提交给 115。", true),
	}
}

func rssAutomationRecognitionInputs(defaultInput string) []RSSAutomationVariableProtocol {
	fields := make([]RSSAutomationVariableProtocol, 0, 2)
	if defaultInput != "" {
		fields = append(fields, rssAutomationTemplateVariable("input", "string", "待识别标题", "MoviePilot 标题识别输入。", defaultInput, true))
	}
	fields = append(fields, rssAutomationTemplateVariable("tmdb_id", "string", "辅助 TMDB ID", "留空自动识别，或提供 TMDB ID 辅助识别。", "1396", false))
	return fields
}

func rssAutomationRecognitionOutputs() []RSSAutomationVariableProtocol {
	return []RSSAutomationVariableProtocol{
		rssAutomationVariable("tmdb_id", "string", "TMDB ID", "MoviePilot 识别出的 TMDB ID。", "1396"),
		rssAutomationVariable("title", "string", "媒体标题", "识别出的规范媒体标题。", "绝命毒师"),
		rssAutomationVariable("year", "string", "年份", "媒体首播或上映年份。", "2008"),
		rssAutomationVariable("media_type", "string", "媒体类型", "movie 或 tv。", "tv"),
		rssAutomationVariable("season_episode", "string", "季集信息", "识别出的季集信息。", "S01E01"),
		rssAutomationVariable("category", "string", "媒体分类", "MoviePilot 分类结果。", "欧美剧"),
		rssAutomationVariable("rating", "number", "评分", "识别结果中的评分。", 9.5),
		rssAutomationVariable("quality", "string", "质量", "识别出的资源质量。", "2160p"),
		rssAutomationVariable("poster_url", "string", "海报地址", "TMDB 海报或背景图地址。", "https://image.tmdb.org/example.jpg"),
	}
}

func RSSAutomationNodeProtocols() []RSSAutomationNodeProtocol {
	return append([]RSSAutomationNodeProtocol(nil), rssAutomationNodeProtocols...)
}

func rssAutomationNodeProtocolByType(nodeType string) (RSSAutomationNodeProtocol, bool) {
	for _, protocol := range rssAutomationNodeProtocols {
		if protocol.Type == nodeType {
			return protocol, true
		}
	}
	return RSSAutomationNodeProtocol{}, false
}
