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
	automationTorrentMagnetProtocol(),
	{Type: AutomationNodeTrigger, Label: "触发事件", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("selected_port", "string", "流程出口", "固定为 next。触发事件字段均可通过 $item.* 引用。", "next"),
		automationVariable("title", "string", "事件名称", "RSS 条目标题，或本地 / 115 文件和目录名称。", "Example.Show.S01"),
		automationVariable("download_url", "string", "下载链接", "RSS 条目解析出的种子、磁力或下载链接。", "magnet:?xt=urn:btih:example"),
		automationVariable("detail_url", "string", "详情链接", "RSS 条目的详情页面链接。", "https://example.com/topic/1"),
		automationVariable("path", "string", "事件路径", "本地或 115 监控目录下的完整路径。本地路径以 FilmFusion 服务器可见路径为准。", "/入库/Example.Show.S01"),
		automationVariable("event_id", "string", "本地事件标识", "同一个本地事件重试时保持不变，可供 HTTP 接收方去重。", "local-event-fingerprint"),
		automationVariable("event_type", "string", "本地事件类型", "本地目录事件为 created 或 modified；移动到目录内视为新增。", "created"),
		automationVariable("relative_path", "string", "本地相对路径", "本地事件对象相对于监控根目录的路径。", "动画/Example.S01E01.mkv"),
		automationVariable("root_path", "string", "本地监控根目录", "FilmFusion 服务器上的本地或 NAS 挂载目录。", "/mnt/media/inbox"),
		automationVariable("name", "string", "本地对象名称", "本地文件或目录的名称。", "Example.S01E01.mkv"),
		automationVariable("is_directory", "boolean", "是否目录", "本地事件对象是否为目录。", false),
		automationVariable("size", "integer", "文件大小", "本地文件字节数。", 1048576),
		automationVariable("modified_at", "datetime", "本地修改时间", "本地对象最后修改时间，使用 RFC 3339 格式。", "2026-10-22T12:00:00Z"),
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "触发事件所在的 115 账号。", 1),
		automationVariable("access_method", "string", "访问方式", "115 目录监控固定使用 OpenAPI；本地目录事件不包含此字段。", "openapi"),
		automationVariable("directory_id", "string", "父目录 ID", "新增对象的父目录 ID。", "0"),
		automationVariable("file_ids", "array", "对象 ID", "可直接传给识别和整理节点的 115 对象 ID。", []string{"123456"}),
		automationVariable("file_names", "array", "对象名称", "与 file_ids 对应的文件或目录名称。", []string{"Example.Show.S01"}),
		automationVariable("tasks", "array", "115 媒体批次", "兼容识别与整理节点的已完成媒体批次。", []map[string]any{{"status": 2, "file_id": "123456"}}),
		automationVariable("completed", "boolean", "上传已完成", "仅在对象稳定并完成上传后触发。", true),
	}},
	{Type: AutomationNodeDelay, Label: "延迟执行", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("delay_seconds", "integer", "延迟秒数", "节点配置并实际等待的固定时长。", 600),
		automationVariable("scheduled_at", "datetime", "开始等待时间", "延迟节点首次进入等待状态的时间。", "2026-08-30T12:00:00Z"),
		automationVariable("resume_at", "datetime", "计划继续时间", "后续节点最早可以继续执行的时间。", "2026-08-30T12:10:00Z"),
		automationVariable("resumed_at", "datetime", "实际继续时间", "延迟结束并恢复流程的实际时间。", "2026-08-30T12:10:00Z"),
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
	{Type: AutomationNodeExtractBetween, Label: "文本区间提取", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "string", "输入文本", "从事件字段或上游变量中读取的原始文本。", "$item.title", true),
		automationVariable("start_marker", "string", "前定位词", "从首次出现的前定位词之后开始截取。", "["),
		automationVariable("end_marker", "string", "后定位词", "截取到前定位词之后首次出现的后定位词之前。", "]"),
		automationVariable("value_type", "string", "输出类型", "string、integer、number、boolean、json 或 array。", "string"),
		automationVariable("variable", "string", "输出变量", "截取并转换后写入的 $vars.* 变量名。", "extracted_value"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("found", "boolean", "定位成功", "是否同时找到前定位词及其后的后定位词。", true),
		automationVariable("raw_result", "string", "原始截取值", "两个定位词之间未经类型转换的文本。", "42"),
		automationVariable("result", "any", "转换结果", "按输出类型转换后的最终值。", 42),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的区间提取结果。", 42)...)},
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
	{Type: AutomationNodeSetVariable, Label: "设置变量", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("value", "any", "变量值", "普通值按字面量写入；精确 $ 引用保留原类型；模板输出文本。", "$item.title", true),
		automationVariable("variable", "string", "变量名", "写入 $vars.* 的安全变量名。", "normalized_title"),
	}, Outputs: automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的值。", "Example S01E01")},
	{Type: AutomationNodeTemplate, Label: "文本模板", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("template", "string", "模板", "使用 {{item.*}}、{{vars.*}} 或 {{nodes.*}} 拼接文本。", "{{item.title}} - {{vars.quality}}", true),
		automationVariable("variable", "string", "变量名", "模板结果写入的 $vars.* 变量名。", "display_title"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("result", "string", "模板结果", "模板解析后的完整文本。", "Example S01E01 - 2160p"),
		automationVariable("missing_references", "array", "缺失引用", "模板中未能解析的变量引用。", []string{"vars.quality"}),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的模板文本。", "Example S01E01 - 2160p")...)},
	{Type: AutomationNodeJSONExtract, Label: "JSON 提取", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "any", "JSON 输入", "JSON 文本、对象、数组或其精确变量引用。", "$nodes.http.output.json", true),
		automationVariable("pointer", "string", "JSON Pointer", "RFC 6901 路径；空字符串表示完整输入。", "/data/items/0/title"),
		automationVariable("variable", "string", "变量名", "提取结果写入的 $vars.* 变量名。", "remote_title"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("found", "boolean", "是否找到", "JSON Pointer 是否命中输入中的值。", true),
		automationVariable("pointer", "string", "JSON Pointer", "本次实际使用的 RFC 6901 路径。", "/data/items/0/title"),
		automationVariable("result", "any", "提取结果", "命中的 JSON 值或配置的默认值。", "Example S01E01"),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的 JSON 提取结果。", "Example S01E01")...)},
	{Type: AutomationNodeMath, Label: "数学运算", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("left", "number", "左值", "固定数字或精确流程变量引用。", "$vars.file_size", true),
		automationTemplateVariable("right", "number", "右值", "二元运算使用的固定数字或精确流程变量引用。", 1024, false),
		automationVariable("variable", "string", "变量名", "数学结果写入的 $vars.* 变量名。", "total_size"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("operation", "string", "运算", "本次执行的数学操作。", "add"),
		automationVariable("result", "number", "运算结果", "完成运算和显式精度处理后的有限数字。", 2048.5),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的数学结果。", 2048.5)...)},
	{Type: AutomationNodeDatetimeOperation, Label: "日期时间运算", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "any", "日期时间", "日期文本、Unix 时间戳或精确流程变量引用。", "$item.published_at", true),
		automationTemplateVariable("right", "any", "比较时间", "diff 操作使用的另一日期时间。", "$vars.baseline_at", false),
		automationVariable("variable", "string", "变量名", "日期时间结果写入的 $vars.* 变量名。", "normalized_at"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("operation", "string", "操作", "本次执行的日期时间操作。", "format"),
		automationVariable("result", "any", "结果", "格式化日期时间或日期时间差。", "2026-08-30T20:00:00+08:00"),
		automationVariable("timezone", "string", "时区", "解析和格式化使用的 IANA 时区。", "Asia/Shanghai"),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的日期时间结果。", "2026-08-30T20:00:00+08:00")...)},
	{Type: AutomationNodeListOperation, Label: "列表处理", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "any", "列表输入", "数组、JSON 数组文本或 split 使用的普通文本。", "$vars.items", true),
		automationVariable("pointer", "string", "JSON Pointer", "unique、sort、pluck 可按 RFC 6901 子字段处理。", "/name"),
		automationVariable("variable", "string", "变量名", "列表结果写入的 $vars.* 变量名。", "processed_items"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("operation", "string", "操作", "本次执行的列表操作。", "unique"),
		automationVariable("result", "any", "结果", "列表、文本或列表长度结果。", []string{"a", "b"}),
		automationVariable("input_count", "integer", "输入数量", "输入列表或分段的项目数。", 3),
		automationVariable("output_count", "integer", "输出数量", "输出结果包含的项目数。", 2),
		automationVariable("empty", "boolean", "是否为空", "结果是否为空并选择 empty 出口。", false),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的列表处理结果。", []string{"a", "b"})...)},
	{Type: AutomationNodeSwitch, Label: "多路判断", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "any", "比较输入", "按顺序与 cases 比较的事件字段或流程变量。", "$vars.quality", true),
		automationVariable("cases", "array", "分支条件", "1 到 20 个有稳定 ID 的有序条件。", []map[string]any{{"id": "uhd", "label": "4K", "operator": "eq", "value": "2160p"}}),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("matched", "boolean", "是否命中", "是否有 case 首次命中。", true),
		automationVariable("case_id", "string", "分支 ID", "首个命中的 case ID。", "uhd"),
		automationVariable("case_label", "string", "分支名称", "首个命中的 case 名称。", "4K"),
		automationVariable("case_index", "integer", "分支序号", "首个命中 case 的零基序号。", 0),
		automationVariable("value", "any", "比较值", "本次 Switch 实际比较的输入值。", "2160p"),
		automationVariable("selected_port", "string", "流程出口", "case-<id>、default 或 failure。", "case-uhd"),
	}},
	{Type: AutomationNodeCoalesce, Label: "选择首个有效值", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("candidates", "array", "候选值", "按顺序解析，选择首个未被跳过的有效值。", []any{"$vars.custom_title", "$item.title"}, true),
		automationVariable("variable", "string", "变量名", "选择结果写入的 $vars.* 变量名。", "effective_title"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("result", "any", "结果", "首个有效候选值或默认值。", "Example S01E01"),
		automationVariable("selected_index", "integer", "候选序号", "命中候选的零基序号；默认值为 -1。", 1),
		automationVariable("used_default", "boolean", "使用默认值", "是否在候选耗尽后使用 default_value。", false),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的首个有效值。", "Example S01E01")...)},
	{Type: AutomationNodeDeduplicate, Label: "运行去重", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("key", "any", "去重键", "文本、数字、布尔值或精确变量引用；仅持久化 SHA-256 摘要。", "$item.guid", true),
		automationVariable("scope", "string", "作用域", "source、workflow 或 global；global 必须配置命名空间。", "workflow"),
		automationVariable("namespace", "string", "命名空间", "隔离不同业务用途的去重记录。", "media-entry"),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("duplicate", "boolean", "是否重复", "键是否已被仍在有效期内的成功运行处理。", false),
		automationVariable("key_hash", "string", "键摘要", "去重键的 SHA-256 摘要，不返回原始键。", "sha256:..."),
		automationVariable("state", "string", "记录状态", "首次命中先保留，流程成功后提交。", "reserved"),
		automationVariable("expires_at", "datetime", "过期时间", "已提交记录的 TTL 到期时间。", "2026-09-06T12:00:00Z"),
	}},
	{Type: AutomationNodeRateLimit, Label: "频率限制", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("key", "any", "限流键", "文本、数字、布尔值或精确变量引用；仅持久化 SHA-256 摘要。", "$item.account_id", true),
		automationVariable("scope", "string", "作用域", "source、workflow 或 global；global 必须配置命名空间。", "workflow"),
		automationVariable("limit", "integer", "窗口额度", "固定 UTC 窗口内允许通过的次数。", 5),
		automationVariable("window_seconds", "integer", "窗口秒数", "固定 UTC 窗口长度。", 60),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("allowed", "boolean", "是否允许", "本次运行是否成功消费额度。", true),
		automationVariable("used", "integer", "已用额度", "当前窗口已消费次数。", 1),
		automationVariable("remaining", "integer", "剩余额度", "当前窗口剩余可用次数。", 4),
		automationVariable("window_end", "datetime", "窗口结束", "当前固定 UTC 窗口结束时间。", "2026-08-30T12:01:00Z"),
		automationVariable("retry_at", "datetime", "重试时间", "受限时最早可重试时间。", "2026-08-30T12:01:00Z"),
	}},
	{Type: AutomationNodeForeach, Label: "遍历转换", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("input", "array", "输入列表", "最多 1000 项的数组或 JSON 数组变量。", "$vars.items", true),
		automationVariable("transform", "object", "转换器", "白名单纯节点及其 config；每项可引用 $each.*。", map[string]any{"type": "template", "config": map[string]any{"template": "{{each.index}}:{{each.item}}"}}),
		automationVariable("variable", "string", "变量名", "有序转换结果写入的 $vars.* 变量名。", "mapped_items"),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("results", "array", "结果列表", "与输入索引对齐的转换结果；collect 失败项为 null。", []any{"0:a", nil, "2:c"}),
		automationVariable("count", "integer", "项目总数", "本次 Foreach 输入项目数。", 3),
		automationVariable("succeeded_count", "integer", "成功数", "转换成功的项目数量。", 2),
		automationVariable("failed_count", "integer", "失败数", "转换失败的项目数量。", 1),
		automationVariable("errors", "array", "错误明细", "collect 模式保留项目索引和错误原因。", []map[string]any{{"index": 1, "reason": "字段不存在"}}),
		automationVariable("selected_port", "string", "流程出口", "success、partial、empty 或 failure。", "partial"),
	}, automationVariableWriterProtocolOutputs("变量值", "最终写入流程变量的有序 Foreach 结果。", []any{"0:a", nil, "2:c"})...)},
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
	{Type: AutomationNodeQBittorrent, Label: "添加 qBittorrent 任务", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "下载地址", "磁力链接或 HTTP/HTTPS 种子地址。", "$item.download_url", true),
		automationTemplateVariable("save_path", "string", "保存路径", "qBittorrent 保存路径，支持模板变量。", "/downloads/{{item.category}}", false),
		automationTemplateVariable("category", "string", "分类", "qBittorrent 分类，支持模板变量。", "{{item.category}}", false),
		automationTemplateVariable("tags", "string", "标签", "逗号分隔的 qBittorrent 标签，支持模板变量。", "automation,{{nodes.mp.output.media_type}}", false),
		automationTemplateVariable("rename", "string", "任务名称", "可选的 Torrent 显示名称，支持模板变量。", "{{item.title}}", false),
		automationVariable("paused", "boolean", "暂停添加", "添加后先保持暂停。", false),
		automationVariable("sequential", "boolean", "顺序下载", "启用 qBittorrent 顺序下载。", false),
		automationVariable("skip_checking", "boolean", "跳过校验", "仅在已有完整下载数据时跳过哈希校验。", false),
		automationVariable("first_last_piece_priority", "boolean", "优先首尾块", "优先下载首尾数据块。", false),
		automationVariable("root_folder", "string", "根目录策略", "default、true 或 false。", "default"),
		automationVariable("auto_tmm", "string", "自动 Torrent 管理", "default、true 或 false。", "default"),
		automationVariable("download_limit_kib", "integer", "下载限速", "每秒 KiB；留空继承 qBittorrent 配置，0 表示不限速。", 10240),
		automationVariable("upload_limit_kib", "integer", "上传限速", "每秒 KiB；留空继承 qBittorrent 配置，0 表示不限速。", 1024),
		automationVariable("ratio_limit", "number", "分享率限制", "-2 使用全局设置，-1 不限制，或填写 0 到 1000。", 2),
		automationVariable("seeding_time_limit", "integer", "做种时间限制", "分钟；-2 使用全局设置，-1 不限制。", 1440),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("target_id", "integer", "下载器 ID", "所使用的下载器账号 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "所使用的下载器账号名称。", "家庭 NAS qB"),
		automationVariable("content_key", "string", "内容键", "下载地址计算出的稳定去重键。", "e3b0c442..."),
		automationVariable("torrent_tag", "string", "跟踪标签", "等待节点用于定位任务的内部标签。", "filmfusion-automation-e3b0c442"),
		automationVariable("submitted", "boolean", "已提交", "qBittorrent 是否接受了添加请求。", true),
		automationVariable("accepted", "boolean", "已接受", "添加请求是否至少有一个成功或等待处理的任务。", true),
		automationVariable("pending", "boolean", "等待处理", "WebAPI 是否返回了等待异步处理的任务。", false),
		automationVariable("success_count", "integer", "成功数", "qBittorrent WebAPI 2.14+ 返回的成功任务数；旧版响应按单任务记为 1。", 1),
		automationVariable("pending_count", "integer", "等待数", "qBittorrent WebAPI 2.14+ 返回的等待处理任务数。", 0),
		automationVariable("failure_count", "integer", "失败数", "qBittorrent WebAPI 2.14+ 返回的失败任务数。", 0),
		automationVariable("torrent_hashes", "array", "Torrent Hash 列表", "WebAPI 2.14+ 返回的已添加 Torrent ID；等待节点优先用它精确定位任务。", []string{"0123456789abcdef"}),
		automationVariable("hash", "string", "首个 Torrent Hash", "首个已添加任务的 Torrent ID；旧版响应可能为空。", "0123456789abcdef"),
		automationVariable("response_status", "integer", "HTTP 状态码", "qBittorrent 添加接口返回的 HTTP 状态码。", 200),
		automationVariable("response_format", "string", "响应格式", "legacy 或 webapi_2_14。", "webapi_2_14"),
	}},
	{Type: AutomationNodeWaitQBittorrent, Label: "等待 qBittorrent 完成", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("target_id", "integer", "下载器 ID", "任务所在的 qBittorrent 下载器 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "任务所在的 qBittorrent 下载器名称。", "家庭 NAS qB"),
		automationVariable("torrent_tag", "string", "跟踪标签", "FilmFusion 提交任务时附加的内部跟踪标签。", "filmfusion-automation-e3b0c442"),
		automationVariable("torrent_hashes", "array", "Torrent Hash 列表", "添加节点返回的任务哈希；新版 WebAPI 优先使用它精确查询。", []string{"0123456789abcdef"}),
		automationVariable("waiting", "boolean", "等待中", "任务是否仍在等待下一次轮询。", false),
		automationVariable("completed", "boolean", "是否完成", "qBittorrent 下载是否完成。", true),
		automationVariable("failed", "boolean", "任务异常", "qBittorrent 是否返回错误或文件缺失状态。", false),
		automationVariable("timed_out", "boolean", "等待超时", "是否超过节点配置的最长等待时间。", false),
		automationVariable("progress", "number", "下载进度", "0 到 100 的下载进度。", 100),
		automationVariable("state", "string", "任务状态", "qBittorrent 返回的任务状态。", "uploading"),
		automationVariable("hash", "string", "Torrent Hash", "qBittorrent 任务哈希。", "0123456789abcdef"),
		automationVariable("name", "string", "任务名称", "qBittorrent 任务名称。", "Example.Movie.2026"),
		automationVariable("save_path", "string", "保存目录", "qBittorrent 保存目录。", "/downloads/movies"),
		automationVariable("content_path", "string", "完成路径", "下载完成后的文件或文件夹路径。", "/downloads/movies/Example.Movie.2026"),
		automationVariable("content_type", "string", "完成路径类型", "下载结果是 file 或 dir。", "dir"),
		automationVariable("file_count", "integer", "任务文件数", "Torrent 中包含的文件数量。", 12),
		automationVariable("size", "integer", "文件大小", "任务总字节数。", 10737418240),
		automationVariable("downloaded", "integer", "已下载字节", "qBittorrent 已下载的字节数。", 10737418240),
		automationVariable("amount_left", "integer", "剩余字节", "qBittorrent 尚未下载的字节数。", 0),
		automationVariable("uploaded", "integer", "已上传字节", "qBittorrent 已上传的字节数。", 5368709120),
		automationVariable("download_speed", "integer", "下载速度", "当前下载速度，单位字节/秒。", 0),
		automationVariable("upload_speed", "integer", "上传速度", "当前上传速度，单位字节/秒。", 1048576),
		automationVariable("ratio", "number", "分享率", "当前上传分享率。", 1.25),
		automationVariable("eta", "integer", "预计剩余秒数", "qBittorrent 返回的 ETA 秒数。", 0),
		automationVariable("category", "string", "分类", "任务当前所属分类。", "电影"),
		automationVariable("tags", "string", "标签", "任务当前标签字符串。", "automation,电影"),
		automationVariable("tracker", "string", "Tracker", "任务当前 Tracker 地址。", "https://tracker.example/announce"),
		automationVariable("seeders", "integer", "做种者", "当前连接或可见的做种者数量。", 12),
		automationVariable("leechers", "integer", "下载者", "当前连接或可见的下载者数量。", 3),
		automationVariable("added_on", "integer", "添加时间", "Unix 时间戳。", 1788336000),
		automationVariable("completion_on", "integer", "完成时间", "Unix 时间戳；未完成时通常为 -1。", 1788339600),
		automationVariable("last_activity", "integer", "最后活动时间", "Unix 时间戳。", 1788340000),
		automationVariable("waiting_since", "datetime", "开始等待时间", "等待节点首次开始轮询的时间。", "2026-09-02T12:00:00Z"),
		automationVariable("last_checked_at", "datetime", "最近检查时间", "最近一次查询 qBittorrent 的时间。", "2026-09-02T12:00:30Z"),
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
	{Type: AutomationNodeStopQBittorrent, Label: "停止 qBittorrent 做种", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("stopped", "boolean", "停止成功", "qBittorrent 任务是否已停止做种。", true),
		automationVariable("already_stopped", "boolean", "原本已停止", "执行时任务是否已经处于停止或暂停状态。", false),
		automationVariable("already_missing", "boolean", "原本已不存在", "执行时任务是否已经不在 qBittorrent 中。", false),
		automationVariable("files_retained", "boolean", "保留下载文件", "停止做种不会删除 qBittorrent 任务或下载数据。", true),
		automationVariable("hash", "string", "Torrent Hash", "被停止的 qBittorrent 任务哈希。", "0123456789abcdef"),
		automationVariable("name", "string", "任务名称", "停止前查询到的 qBittorrent 任务名称。", "Example.Movie.2026"),
		automationVariable("content_path", "string", "内容路径", "停止前查询到的下载内容路径。", "/downloads/Example.Movie.2026"),
		automationVariable("previous_state", "string", "停止前状态", "qBittorrent 返回的停止前任务状态。", "uploading"),
		automationVariable("target_id", "integer", "下载器 ID", "执行停止操作的 qBittorrent 下载器 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "执行停止操作的 qBittorrent 下载器名称。", "家庭 NAS qB"),
		automationVariable("api_action", "string", "API 动作", "qBittorrent 5 使用 stop，旧版兼容回退为 pause。", "stop"),
		automationVariable("response_status", "integer", "HTTP 状态码", "qBittorrent 停止接口返回的 HTTP 状态码；任务不存在或原本已停止时为空。", 200),
		automationVariable("stopped_at", "datetime", "停止时间", "实际向 qBittorrent 提交停止操作的时间。", "2026-09-04T12:00:00Z"),
	}},
	{Type: AutomationNodeDeleteQBittorrent, Label: "删除 qBittorrent 任务", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("deleted", "boolean", "删除成功", "qBittorrent 做种任务是否已删除。", true),
		automationVariable("already_missing", "boolean", "原本已不存在", "执行时任务是否已经不在 qBittorrent 中。", false),
		automationVariable("delete_files", "boolean", "同时删除文件", "是否要求 qBittorrent 一并删除下载数据。", false),
		automationVariable("hash", "string", "Torrent Hash", "被删除的 qBittorrent 任务哈希。", "0123456789abcdef"),
		automationVariable("name", "string", "任务名称", "删除前查询到的 qBittorrent 任务名称。", "Example.Movie.2026"),
		automationVariable("content_path", "string", "内容路径", "删除前查询到的下载内容路径。", "/downloads/Example.Movie.2026"),
		automationVariable("target_id", "integer", "下载器 ID", "执行删除的 qBittorrent 下载器 ID。", 1),
		automationVariable("target_name", "string", "下载器名称", "执行删除的 qBittorrent 下载器名称。", "家庭 NAS qB"),
		automationVariable("response_status", "integer", "HTTP 状态码", "qBittorrent 删除接口返回的 HTTP 状态码；任务原本不存在时为空。", 200),
		automationVariable("deleted_at", "datetime", "删除时间", "实际向 qBittorrent 提交删除的时间。", "2026-08-16T12:00:00Z"),
	}},
	{Type: AutomationNodeOffline115, Label: "115 云下载（Cookie）", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "下载地址", "提交到 115 的下载地址。", "$item.download_url", true),
	}, Outputs: automation115SubmitOutputs()},
	{Type: AutomationNodeOffline115OpenAPI, Label: "115 添加链接任务", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "下载链接", "支持磁力、HTTP(S)、FTP、电驴；多个链接换行分隔，每批最多 115 个。", "$item.download_url", true),
	}, Outputs: automation115LinkOutputs()},
	{Type: AutomationNodeTorrent115OpenAPI, Label: "115 解析 BT 种子", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("torrent_sha1", "string", "种子文件 SHA1", "已上传到当前账号的种子文件 SHA1，不是 BT info_hash。", "$vars.torrent_sha1", true),
		automationTemplateVariable("pick_code", "string", "种子提取码", "当前账号内种子文件的 pick_code。", "$vars.pick_code", true),
	}, Outputs: automation115CloudOutputs("object",
		automationVariable("info_hash", "string", "BT 任务 Hash", "种子解析出的 info_hash，用于添加 BT 任务。", "abcdef"),
		automationVariable("torrent_name", "string", "种子任务名称", "解析得到的任务名称，可作为 BT 保存路径。", "Example.Show"),
		automationVariable("file_size", "integer", "任务大小", "种子内文件总大小（字节）。", 1024),
		automationVariable("file_count", "integer", "文件数量", "种子包含的文件数量。", 1),
		automationVariable("torrent_filelist", "array", "种子文件列表", "保留 size、path、wanted；数组索引用于添加任务时选择下载文件。", []any{}),
		automationVariable("wanted", "string", "默认选中文件索引", "将 wanted=1 的文件索引以逗号拼接；未默认选中时为空，需自行选择。", "0,1"),
		automationVariable("torrent_sha1", "string", "种子文件 SHA1", "本次解析输入的种子文件 SHA1。", "abc123"),
		automationVariable("pick_code", "string", "种子提取码", "本次解析输入的种子提取码。", "pick123"),
	)},
	{Type: AutomationNodeOffline115BTOpenAPI, Label: "115 添加 BT 任务", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("info_hash", "string", "BT 任务 Hash", "引用种子解析节点的 info_hash。", "{{nodes.torrent.output.info_hash}}", true),
		automationTemplateVariable("wanted", "string", "下载文件索引", "以逗号分隔的文件索引；0 是有效值，不可为空。", "0,1", true),
		automationTemplateVariable("save_path", "string", "相对保存路径", "相对于保存目录的路径，例如 A/B。", "Example.Show", true),
		automationTemplateVariable("torrent_sha1", "string", "种子文件 SHA1", "当前账号内种子文件的 SHA1。", "{{nodes.torrent.output.torrent_sha1}}", true),
		automationTemplateVariable("pick_code", "string", "种子提取码", "当前账号内种子文件的提取码。", "{{nodes.torrent.output.pick_code}}", true),
	}, Outputs: append(append(automation115SubmitOutputs(), automation115ResponseOutputs("array")...),
		automationVariable("info_hash", "string", "提交任务 Hash", "提交成功后透传输入 Hash；115 此接口原始 data 为空数组。", "abcdef"),
		automationVariable("save_path", "string", "相对保存路径", "本次提交的 BT 保存路径。", "Example.Show"),
	)},
	{Type: AutomationNodeList115TasksOpenAPI, Label: "115 云下载任务列表", Inputs: []AutomationVariableProtocol{
		automationVariable("page", "integer", "查询页码", "未指定 Hash 时查询这一页，默认 1。", 1),
		automationTemplateVariable("info_hash", "string", "查找任务 Hash", "可选。指定后从第一页逐页精确匹配，最多 200 页；找到后可连接等待节点。", "{{nodes.download.output.info_hash}}", false),
	}, Outputs: automation115TaskListOutputs()},
	{Type: AutomationNodeQuota115OpenAPI, Label: "115 云下载配额", Inputs: []AutomationVariableProtocol{}, Outputs: automation115CloudOutputs("object",
		automationVariable("count", "integer", "总配额", "用户总云下载配额。", 100),
		automationVariable("surplus", "integer", "剩余配额", "用户总剩余云下载配额。", 80),
		automationVariable("used", "integer", "已用配额", "用户总已用云下载配额。", 20),
		automationVariable("package", "array", "配额明细", "保留各类型的 surplus、used、count、name 和 expire_info 过期明细。", []any{}),
	)},
	{Type: AutomationNodeDelete115TaskOpenAPI, Label: "115 删除云下载任务", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("info_hash", "string", "删除任务 Hash", "要删除的单个任务 info_hash。", "{{nodes.download.output.info_hash}}", true),
		automationVariable("delete_files", "boolean", "同时删除源文件", "默认 false，仅删除任务；true 同时删除对应源文件。", false),
	}, Outputs: automation115CloudOutputs("array",
		automationVariable("deleted", "boolean", "任务已删除", "115 确认删除成功。", true),
		automationVariable("info_hash", "string", "删除任务 Hash", "本次删除目标的 Hash。", "abcdef"),
		automationVariable("delete_files", "boolean", "同时删除源文件", "本次是否要求删除源文件。", false),
	)},
	{Type: AutomationNodeClear115TasksOpenAPI, Label: "115 清空云下载任务", Inputs: []AutomationVariableProtocol{
		automationVariable("flag", "integer", "清理范围", "0 完成、1 全部、2 失败、3 进行中、4 完成并删源文件、5 全部并删源文件。", 0),
	}, Outputs: automation115CloudOutputs("array",
		automationVariable("cleared", "boolean", "任务已清空", "115 确认清空成功。", true),
		automationVariable("flag", "integer", "清理范围", "本次提交的清理类型。", 0),
	)},
	{Type: AutomationNodeWait115, Label: "等待 115 下载完成", Inputs: []AutomationVariableProtocol{}, Outputs: []AutomationVariableProtocol{
		automationVariable("info_hash", "string", "单任务 Hash", "仅等待单个任务时返回其 Hash。", "abcdef"),
		automationVariable("completed", "boolean", "是否下载完成", "所有 115 云下载任务是否完成。", true),
		automationVariable("percent", "number", "下载进度", "所有任务的平均下载进度。", 100),
		automationVariable("file_id", "string", "已完成对象 ID", "仅单任务完成时返回的文件或文件夹 ID，多任务请使用 file_ids。", "123456"),
		automationVariable("file_name", "string", "首个完成文件名", "单任务完成时返回的文件或文件夹名称。", "Example.Movie.2026"),
		automationVariable("file_ids", "array", "完成文件 ID", "全部完成任务的文件或文件夹 ID。", []string{"123456"}),
		automationVariable("file_names", "array", "完成文件名", "全部完成任务的文件或文件夹名称。", []string{"Example.Movie.2026"}),
		automationVariable("directory_id", "string", "下载目录 ID", "115 离线任务保存目录 ID。", "0"),
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行下载的云存储账号 ID。", 1),
		automationVariable("tasks", "array", "完成任务", "115 任务详情；OpenAPI 任务的 response 保留该任务所有原始字段。", []any{}),
	}},
	{Type: AutomationNodeRename115OpenAPI, Label: "115 API 重命名", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("file_id", "string", "文件或文件夹 ID", "要通过 115 OpenAPI 重命名的对象 ID，通常引用等待节点的 file_id。", "{{nodes.wait115.output.file_id}}", true),
		automationTemplateVariable("new_name", "string", "新名称", "完整的新文件名或文件夹名；重命名文件时请按需保留扩展名。", "{{item.title}}.mkv", true),
	}, Outputs: append([]AutomationVariableProtocol{
		automationVariable("renamed", "boolean", "重命名成功", "115 OpenAPI 是否已成功完成重命名。", true),
		automationVariable("file_id", "string", "文件或文件夹 ID", "已重命名对象的 115 ID。", "123456"),
		automationVariable("file_name", "string", "实际名称", "115 OpenAPI 返回的实际文件名或文件夹名。", "示例剧.S01E01.mkv"),
		automationVariable("new_name", "string", "请求名称", "本次提交给 115 OpenAPI 的新名称。", "示例剧.S01E01.mkv"),
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行重命名的云存储账号 ID。", 1),
		automationVariable("storage_name", "string", "115 账号名称", "执行重命名的账号名称。", "115 主账号"),
		automationVariable("access_method", "string", "访问方式", "固定为 openapi。", "openapi"),
	}, automation115ResponseOutputs("object")...)},
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
	{Type: AutomationNodeMoviePilotRecognize, Label: "MP2 / 影子识别", Inputs: automationRecognitionInputs(""), Outputs: append(automationRecognitionOutputs(),
		automationVariable("requested_tmdb_id", "string", "辅助 TMDB ID", "本次识别实际使用的辅助 TMDB ID；留空自动识别时为空。", "1396"),
		automationVariable("total_files", "integer", "媒体文件数", "115 下载结果中找到的可识别媒体文件数量。", 1),
		automationVariable("recognized_count", "integer", "识别文件数", "成功识别的媒体文件数量。", 1),
		automationVariable("failed_count", "integer", "识别失败数", "未能识别的媒体文件数量。", 0),
		automationVariable("items", "array", "识别结果", "每个媒体文件的识别详情。", []any{}),
		automationVariable("failed_items", "array", "识别失败明细", "未能识别的媒体文件及原因。", []any{}),
		automationVariable("partial", "boolean", "部分识别", "是否只有部分文件识别成功。", false),
	)},
	{Type: AutomationNodeOrganizeStrm, Label: "整理生成 STRM", Inputs: []AutomationVariableProtocol{
		automationVariable("recognition_source", "string", "整理引擎", "moviepilot、medianame、shadow；留空跟随目录或系统设置。", "shadow"),
		automationVariable("dry_run", "boolean", "只生成预览", "生成完整计划，不移动文件或写 STRM。", false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("recognition_source", "string", "固定引擎", "本任务识别和整理使用同一模式。", "shadow"),
		automationVariable("dry_run", "boolean", "仅预览", "为 true 时仅生成计划，没有文件操作。", false),
		automationVariable("planned_count", "integer", "预览文件数", "只预览时有效计划中的媒体数量。", 1),
		automationVariable("plans", "array", "计划快照", "各批次 plan_id、包版本和配置快照；items 内包含完整路径与影子诊断。", []any{}),
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

func automationVariableWriterProtocolOutputs(valueLabel, valueDescription string, example any) []AutomationVariableProtocol {
	return []AutomationVariableProtocol{
		automationVariable("variable", "string", "变量名", "本节点尝试写入的 $vars.* 变量名。", "normalized_title"),
		automationVariable("value", "any", valueLabel, valueDescription, example),
		automationVariable("value_type", "string", "实际类型", "写入值最终持久化的 JSON 类型。", "string"),
		automationVariable("existing", "boolean", "原值是否存在", "执行覆盖策略前是否已经存在同名流程变量。", false),
		automationVariable("written", "boolean", "是否写入", "覆盖策略执行后是否实际写入变量。", true),
		automationVariable("overwritten", "boolean", "是否覆盖", "本次写入是否覆盖了已有变量。", false),
		automationVariable("variables", "object", "生成变量", "本节点实际写入 $vars.* 的变量对象。", map[string]any{"normalized_title": example}),
	}
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
	fields = append(fields, automationVariable("recognition_source", "string", "识别方式", "moviepilot、medianame、local 或 shadow；留空跟随系统设置。", "shadow"))
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
