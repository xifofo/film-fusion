package service

func automation115ResponseOutputs(dataType string) []AutomationVariableProtocol {
	var example any = []any{}
	if dataType == "object" {
		example = map[string]any{}
	}
	return []AutomationVariableProtocol{
		automationVariable("state", "boolean", "接口状态", "115 原始响应 state；链接提交还需检查逐条结果。", true),
		automationVariable("code", "integer", "接口错误码", "115 原始响应 code，成功为 0。", 0),
		automationVariable("message", "string", "接口返回信息", "115 原始响应 message，成功时通常为空。", "错误说明"),
		automationVariable("data", dataType, "接口响应数据", "115 原始 data，保留全部响应字段，可用 JSON 取值节点或点路径引用。", example),
		automationVariable("response", "object", "完整接口响应", "包含 state、code、message、data 的原始响应，不含认证信息。", map[string]any{"state": true, "code": 0, "message": "", "data": []any{}}),
	}
}

func automation115CloudOutputs(dataType string, fields ...AutomationVariableProtocol) []AutomationVariableProtocol {
	outputs := append(automation115ResponseOutputs(dataType),
		automationVariable("cloud_storage_id", "integer", "115 账号 ID", "执行节点的 115 云存储账号 ID。", 1),
		automationVariable("storage_name", "string", "115 账号名称", "执行节点的 115 云存储账号名称。", "115 主账号"),
		automationVariable("access_method", "string", "访问方式", "固定为 openapi。", "openapi"),
	)
	return append(outputs, fields...)
}

func automation115LinkOutputs() []AutomationVariableProtocol {
	outputs := append(automation115SubmitOutputs(), automation115ResponseOutputs("array")...)
	return append(outputs,
		automationVariable("info_hash", "string", "单个成功任务 Hash", "仅一个链接成功时提供；重命名仍需等待任务完成后取得 file_id。", "abcdef"),
		automationVariable("results", "array", "逐链接结果", "每项包含 state、code、message、info_hash、url；与原始 data 一致。", []any{}),
		automationVariable("failed_results", "array", "失败链接结果", "提交失败或未返回任务 Hash 的链接结果。", []any{}),
		automationVariable("success_count", "integer", "提交成功数量", "已返回任务 Hash 的链接数量。", 1),
		automationVariable("failure_count", "integer", "提交失败数量", "已返回失败结果的链接数量。", 0),
		automationVariable("partial", "boolean", "部分成功", "部分成功走失败出口，已成功的链接不会自动重复提交。", false),
	)
}

func automation115TaskListOutputs() []AutomationVariableProtocol {
	return automation115CloudOutputs("object",
		automationVariable("page", "integer", "当前页码", "最近一次列表响应的页码。", 1),
		automationVariable("page_count", "integer", "任务总页数", "115 返回的任务总页数。", 2),
		automationVariable("count", "integer", "任务总数量", "115 返回的任务总数，不是匹配数量。", 20),
		automationVariable("tasks", "array", "任务列表", "未指定 Hash 时返回当前页；指定 Hash 时仅含匹配任务。保留每项全部字段，包括 percentDone、file_id、wp_path_id 等。", []any{}),
		automationVariable("responses", "array", "各页原始响应", "按 Hash 查找时实际读取的各页完整响应。", []any{}),
		automationVariable("pages_checked", "integer", "已查询页数", "本次查找实际读取的页数。", 1),
		automationVariable("matched", "boolean", "找到目标任务", "配置 info_hash 时，是否在任务列表中找到目标。", true),
		automationVariable("task", "object", "匹配任务详情", "按 Hash 匹配的完整原始任务；例如 task.url、task.add_time、task.delete_file_id。", map[string]any{"info_hash": "abcdef", "status": 2, "percentDone": 100, "file_id": "123456", "name": "Example.mkv", "wp_path_id": "0"}),
		automationVariable("info_hash", "string", "查询任务 Hash", "配置并解析的目标 Hash。", "abcdef"),
		automationVariable("hashes", "array", "查询任务 Hash 列表", "供后续等待节点持续查询目标任务。", []string{"abcdef"}),
		automationVariable("status", "integer", "目标下载状态", "-1 失败、0 分配中、1 下载中、2 下载成功；未找到时不返回。", 2),
		automationVariable("percent", "number", "目标下载进度", "匹配任务的 percentDone。", 100),
		automationVariable("completed", "boolean", "目标下载完成", "仅当匹配任务 status=2 且 file_id 有效时为 true。", true),
		automationVariable("file_id", "string", "已完成对象 ID", "仅目标完成时有值，可交给重命名节点；多文件任务可能对应文件夹。", "123456"),
		automationVariable("file_name", "string", "已完成对象名称", "匹配任务完成后返回的文件或文件夹名。", "Example.mkv"),
		automationVariable("directory_id", "string", "父目录 ID", "匹配任务的 wp_path_id。", "0"),
	)
}
