package service

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

func validateAutomationNodeConfig(node AutomationNode) error {
	config := node.Config
	if config == nil {
		config = map[string]any{}
	}
	switch node.Type {
	case AutomationNodeDelay:
		_, err := automationDelayDuration(config)
		return err
	case AutomationNodeRegex:
		pattern := automationConfigString(config, "pattern")
		if pattern == "" {
			return errors.New("正则表达式不能为空")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("正则表达式无效: %w", err)
		}
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		if valueType := automationConfigString(config, "value_type"); valueType != "" && !isAutomationScalarValueType(valueType) {
			return errors.New("正则输出类型必须是 string/integer/number/boolean/datetime")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeExtractBetween:
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		startMarker := automationConfigRawString(config, "start_marker")
		endMarker := automationConfigRawString(config, "end_marker")
		if strings.TrimSpace(startMarker) == "" || strings.TrimSpace(endMarker) == "" {
			return errors.New("前定位词和后定位词不能为空")
		}
		if len([]rune(startMarker)) > 200 || len([]rune(endMarker)) > 200 {
			return errors.New("前定位词和后定位词不能超过 200 个字符")
		}
		valueType := strings.ToLower(automationConfigString(config, "value_type"))
		if valueType == "" {
			valueType = "string"
		}
		switch valueType {
		case "string", "integer", "number", "boolean", "json", "array":
		default:
			return errors.New("区间提取类型必须是 string/integer/number/boolean/json/array")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeKeyword:
		if automationConfigString(config, "input") == "" {
			return errors.New("必须配置输入字段")
		}
		keywords := automationConfigTextSlice(config, "keywords")
		if len(keywords) == 0 {
			return errors.New("至少需要一个关键词")
		}
		if len(keywords) > 100 {
			return errors.New("关键词不能超过 100 个")
		}
		for _, keyword := range keywords {
			if len([]rune(keyword)) > 200 {
				return errors.New("单个关键词不能超过 200 个字符")
			}
		}
		switch automationConfigString(config, "match_mode") {
		case "contains_any", "contains_all", "contains_none":
		default:
			return errors.New("关键词规则必须是 contains_any/contains_all/contains_none")
		}
		if caseSensitive, exists := config["case_sensitive"]; exists {
			if _, ok := caseSensitive.(bool); !ok {
				return errors.New("区分大小写配置必须是布尔值")
			}
		}
	case AutomationNodeKeywordReplace:
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		rules, err := automationConfigReplacementRules(config, "replacements")
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			return errors.New("至少需要一条关键词替换规则")
		}
		if len(rules) > 100 {
			return errors.New("关键词替换规则不能超过 100 条")
		}
		for _, rule := range rules {
			if len([]rune(rule.Keyword)) > 200 {
				return errors.New("单个替换关键词不能超过 200 个字符")
			}
			if len([]rune(rule.Replacement)) > 2000 {
				return errors.New("单个替换内容不能超过 2000 个字符")
			}
		}
		if caseSensitive, exists := config["case_sensitive"]; exists {
			if _, ok := caseSensitive.(bool); !ok {
				return errors.New("区分大小写配置必须是布尔值")
			}
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeRegexReplace:
		pattern := automationConfigString(config, "pattern")
		if pattern == "" {
			return errors.New("正则表达式不能为空")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("正则表达式无效: %w", err)
		}
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeConvert:
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		switch automationConfigString(config, "value_type") {
		case "string", "integer", "number", "boolean", "datetime":
		default:
			return errors.New("转换类型必须是 string/integer/number/boolean/datetime")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeSetVariable:
		return validateAutomationSetVariableConfig(config)
	case AutomationNodeTemplate:
		return validateAutomationTemplateConfig(config)
	case AutomationNodeJSONExtract:
		return validateAutomationJSONExtractConfig(config)
	case AutomationNodeMath:
		return validateAutomationMathConfig(config)
	case AutomationNodeDatetimeOperation:
		return validateAutomationDatetimeOperationConfig(config)
	case AutomationNodeListOperation:
		return validateAutomationListOperationConfig(config)
	case AutomationNodeSwitch:
		return validateAutomationSwitchConfig(config)
	case AutomationNodeCoalesce:
		return validateAutomationCoalesceConfig(config)
	case AutomationNodeForeach:
		return validateAutomationForeachConfig(config)
	case AutomationNodeDeduplicate:
		return validateAutomationDeduplicateConfig(config)
	case AutomationNodeRateLimit:
		return validateAutomationRateLimitConfig(config)
	case AutomationNodeIf:
		condition, ok := config["condition"]
		if !ok {
			return errors.New("必须配置判断条件")
		}
		if err := validateAutomationConditionShape(condition); err != nil {
			return err
		}
	case AutomationNodeParallel:
		branches := automationConfigStringSlice(config, "branches")
		if len(branches) > 0 {
			if len(branches) < 2 {
				return errors.New("并行节点至少需要两个分支")
			}
			seen := make(map[string]struct{}, len(branches))
			for _, branch := range branches {
				if !strings.HasPrefix(branch, "branch-") || len(branch) == len("branch-") {
					return fmt.Errorf("并行分支 %q 必须以 branch- 开头", branch)
				}
				if _, exists := seen[branch]; exists {
					return fmt.Errorf("并行分支 %q 重复", branch)
				}
				seen[branch] = struct{}{}
			}
		}
	case AutomationNodeJoin:
		switch automationConfigString(config, "policy") {
		case "", "all_completed", "all_success", "any_success", "any_completed":
		default:
			return errors.New("不支持的汇合策略")
		}
	case AutomationNodeQBittorrent:
		if automationConfigUint(config, "target_id") == 0 {
			return errors.New("必须选择 qBittorrent 目标")
		}
		if automationConfigString(config, "url") == "" {
			return errors.New("必须配置下载地址变量")
		}
		if len([]rune(automationConfigString(config, "rename"))) > 255 {
			return errors.New("qBittorrent 任务名称不能超过 255 个字符")
		}
		for _, key := range []string{"paused", "sequential", "skip_checking", "first_last_piece_priority"} {
			if value, exists := config[key]; exists && value != nil {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("qBittorrent 配置 %s 必须是布尔值", key)
				}
			}
		}
		for _, key := range []string{"root_folder", "auto_tmm"} {
			switch strings.ToLower(automationConfigString(config, key)) {
			case "", "default", "true", "false":
			default:
				return fmt.Errorf("qBittorrent 配置 %s 必须是 default/true/false", key)
			}
		}
		for _, key := range []string{"download_limit_kib", "upload_limit_kib"} {
			value, exists, err := automationConfigOptionalInt(config, key)
			if err != nil {
				return err
			}
			if exists && (value < 0 || value > 1_048_576) {
				return fmt.Errorf("qBittorrent 配置 %s 必须在 0 到 1048576 KiB/s 之间", key)
			}
		}
		if value, exists := config["ratio_limit"]; exists && value != nil {
			ratio, ok := automationNumber(value)
			if !ok || math.IsNaN(ratio) || math.IsInf(ratio, 0) || (ratio < 0 && ratio != -1 && ratio != -2) || ratio > 1000 {
				return errors.New("qBittorrent 分享率限制必须是 -2、-1 或 0 到 1000")
			}
		}
		if value, exists, err := automationConfigOptionalInt(config, "seeding_time_limit"); err != nil {
			return err
		} else if exists && ((value < 0 && value != -1 && value != -2) || value > 5_256_000) {
			return errors.New("qBittorrent 做种时间限制必须是 -2、-1 或 0 到 5256000 分钟")
		}
	case AutomationNodeWaitQBittorrent:
		pollSeconds := automationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && (pollSeconds < 5 || pollSeconds > 300) {
			return errors.New("qBittorrent 检查间隔必须在 5 到 300 秒之间")
		}
		if maxWaitMinutes := automationConfigUint(config, "max_wait_minutes"); maxWaitMinutes > 30*24*60 {
			return errors.New("qBittorrent 最长等待不能超过 30 天")
		}
	case AutomationNodeDeleteQBittorrent:
		if value, exists := config["delete_files"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("同时删除下载文件配置必须是布尔值")
			}
		}
	case AutomationNodeOffline115, AutomationNodeOffline115OpenAPI:
		if automationConfigUint(config, "cloud_storage_id") == 0 {
			return errors.New("必须选择 115 账号")
		}
		if automationConfigString(config, "url") == "" {
			return errors.New("必须配置下载地址变量")
		}
	case AutomationNodeTorrentToMagnet:
		return validateAutomationTorrentConfig(config)
	case AutomationNodeTorrent115OpenAPI, AutomationNodeOffline115BTOpenAPI, AutomationNodeList115TasksOpenAPI,
		AutomationNodeQuota115OpenAPI, AutomationNodeDelete115TaskOpenAPI, AutomationNodeClear115TasksOpenAPI:
		return validateAutomation115OpenAPIConfig(node)
	case AutomationNodeWait115:
		pollSeconds := automationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && pollSeconds < minAutomation115PollSeconds {
			return fmt.Errorf("检查间隔不能少于 %d 秒", minAutomation115PollSeconds)
		}
		if pollSeconds > maxAutomation115PollSeconds {
			return fmt.Errorf("检查间隔不能超过 %d 秒", maxAutomation115PollSeconds)
		}
		maxWaitMinutes := automationConfigUint(config, "max_wait_minutes")
		if maxWaitMinutes > maxAutomation115MaxWaitMinutes {
			return fmt.Errorf("最长等待不能超过 %d 分钟", maxAutomation115MaxWaitMinutes)
		}
	case AutomationNodeRename115OpenAPI:
		if automationConfigUint(config, "cloud_storage_id") == 0 {
			return errors.New("必须选择 115 OpenAPI 账号")
		}
		if automationConfigString(config, "file_id") == "" {
			return errors.New("必须配置要重命名的文件或文件夹 ID")
		}
		newName := automationConfigString(config, "new_name")
		if newName == "" {
			return errors.New("必须配置新名称")
		}
		if !strings.HasPrefix(newName, "$") && !strings.Contains(newName, "{{") {
			if err := validateAutomation115RenameName(newName); err != nil {
				return err
			}
		}
	case AutomationNodeMoviePilotTitle:
		if automationConfigString(config, "input") == "" {
			return errors.New("必须配置待识别标题")
		}
		fallthrough
	case AutomationNodeMoviePilotRecognize:
		if source := automationConfigString(config, "recognition_source"); source != "" {
			if _, err := NormalizeMediaRecognitionSource(source); err != nil {
				return err
			}
		}
		tmdbID := automationConfigString(config, "tmdb_id")
		if tmdbID != "" && !strings.HasPrefix(tmdbID, "$") && !strings.Contains(tmdbID, "{{") && !automationTMDBIDPattern.MatchString(tmdbID) {
			return errors.New("TMDB ID 必须是正整数或流程变量")
		}
	case AutomationNodeFilmFusionRecognize:
		mode := automationFilmFusionRecognitionMode(node)
		configuredMode := strings.ToLower(automationConfigString(config, "recognition_mode"))
		if configuredMode != "" && configuredMode != MediaRecognitionModeTitle && configuredMode != MediaRecognitionModeFile {
			return errors.New("本地识别模式必须是 title/file")
		}
		if mode == MediaRecognitionModeTitle && automationConfigString(config, "input") == "" {
			return errors.New("发布标题模式必须配置待识别标题")
		}
		if value, exists := config["lookup_tmdb"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("查询 TMDB 配置必须是布尔值")
			}
		}
		tmdbID := automationConfigString(config, "tmdb_id")
		if tmdbID != "" && !strings.HasPrefix(tmdbID, "$") && !strings.Contains(tmdbID, "{{") && !automationTMDBIDPattern.MatchString(tmdbID) {
			return errors.New("TMDB ID 必须是正整数或流程变量")
		}
	case AutomationNodeMoviePilotTransfer:
		tmdbID := automationConfigString(config, "tmdb_id")
		if tmdbID != "" && !strings.HasPrefix(tmdbID, "$") && !strings.Contains(tmdbID, "{{") && !automationTMDBIDPattern.MatchString(tmdbID) {
			return errors.New("TMDB ID 必须是正整数或流程变量")
		}
		switch automationConfigString(config, "file_type") {
		case "", "auto", "file", "dir":
		default:
			return errors.New("MP2 整理源类型必须是 auto/file/dir")
		}
		switch automationConfigString(config, "media_type") {
		case "", "auto", "movie", "tv":
		default:
			return errors.New("MP2 整理媒体类型必须是 auto/movie/tv")
		}
		if len([]rune(automationConfigString(config, "source_path"))) > 4096 {
			return errors.New("MP2 可见源路径不能超过 4096 个字符")
		}
		if len([]rune(automationConfigString(config, "transfer_type"))) > 64 {
			return errors.New("MP2 整理方式不能超过 64 个字符")
		}
		if value, exists := config["scrape"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("MP2 刮削配置必须是布尔值")
			}
		}
	case AutomationNodeMediaExists:
		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择用于查重的目录配置")
		}
		if automationConfigString(config, "tmdb_id") == "" {
			return errors.New("必须配置 TMDB ID")
		}
	case AutomationNodeOrganizeStrm:
		if source := automationConfigString(config, "recognition_source"); source != "" {
			if _, err := NormalizeMediaRecognitionSource(source); err != nil {
				return err
			}
		}

		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择整理目录配置")
		}
		switch automationConfigString(config, "media_type") {
		case "", "auto", "movie", "tv":
		default:
			return errors.New("媒体类型必须是 auto/movie/tv")
		}
		if len([]rune(automationConfigString(config, "category"))) > 200 {
			return errors.New("媒体分类不能超过 200 个字符")
		}
		for _, key := range []string{"best_version_enabled", "delete_source_folder", "filename_regex_enabled", "dry_run"} {
			if value, exists := config[key]; exists {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("%s 必须是布尔值", key)
				}
			}
		}
		if automationConfigBool(config, "filename_regex_enabled") {
			pattern := automationConfigString(config, "filename_regex_pattern")
			if pattern == "" {
				return errors.New("启用文件名正则后必须填写表达式")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("文件名正则表达式无效: %w", err)
			}
		}
	case AutomationNodeStrmVerify:
		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择 STRM 所属目录配置")
		}
	case AutomationNodeStrmRegenerate:
		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择 STRM 重生成使用的目录配置")
		}
	case AutomationNodeEmbyRefreshWait:
		if automationConfigString(config, "tmdb_id") == "" {
			return errors.New("必须配置等待入库使用的 TMDB ID")
		}
		pollSeconds := automationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && (pollSeconds < 5 || pollSeconds > 300) {
			return errors.New("Emby 检查间隔必须在 5 到 300 秒之间")
		}
		if maxWaitMinutes := automationConfigUint(config, "max_wait_minutes"); maxWaitMinutes > 24*60 {
			return errors.New("Emby 最长等待不能超过 24 小时")
		}
	case AutomationNodeHTTPRequest:
		method := strings.ToUpper(automationConfigString(config, "method"))
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return errors.New("HTTP 方法必须是 GET/POST/PUT/PATCH/DELETE")
		}
		if automationConfigString(config, "url") == "" {
			return errors.New("必须配置 HTTP 请求地址")
		}
		if len(automationConfigString(config, "url")) > 8192 {
			return errors.New("HTTP 请求地址不能超过 8192 字节")
		}
		if len(automationConfigString(config, "body")) > 1024*1024 {
			return errors.New("HTTP 请求体不能超过 1 MiB")
		}
		if _, err := automationHTTPHeaders(config); err != nil {
			return err
		}
	case AutomationNodeNotification:
		if automationConfigString(config, "message") == "" {
			return errors.New("通知内容不能为空")
		}
		channel := automationConfigString(config, "channel")
		if channel != "" && channel != "default" && channel != "telegram" && channel != "webhook" {
			return errors.New("通知渠道必须为 default、telegram 或 webhook")
		}
		if len(automationConfigString(config, "image_url")) > 4096 {
			return errors.New("通知图片地址不能超过 4096 字节")
		}
	}
	return nil
}
