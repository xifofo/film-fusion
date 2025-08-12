package pathhelper

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// 正则表达式用于匹配 Windows 盘符格式
var driveLetterPattern = regexp.MustCompile(`^[a-zA-Z]:[\\/]+`)

func RemoveDriveLetter(path string) string {
	// 检查输入是否为空字符串
	if path == "" {
		return ""
	}

	// 使用预编译的正则表达式移除盘符
	return driveLetterPattern.ReplaceAllString(path, "")
}

func RemoveFirstDir(path string) string {
	// 去掉开头的 /
	trimmedPath := strings.TrimPrefix(path, "/")

	// 找到第一个 / 的位置
	if index := strings.Index(trimmedPath, "/"); index != -1 {
		// 返回第一个 / 之后的部分
		return trimmedPath[index+1:]
	}

	// 如果没有找到 /，说明只有一个目录，返回空字符串
	return ""
}

func EnsureLeadingSlash(path string) string {
	path = ConvertToLinuxPath(path)

	if !strings.HasPrefix(path, "/") {
		path = "/" + path // 不是以 / 开头，加上 /
	}

	return path
}

func ConvertToLinuxPath(windowsPath string) string {
	// 将所有的反斜杠转换成正斜杠
	linuxPath := strings.ReplaceAll(RemoveDriveLetter(windowsPath), "\\", "/")
	return linuxPath
}

func ConvertToWindowsPath(path string) string {
	return strings.ReplaceAll(path, "/", "\\")
}

// IsSubPath 检查 path 是否是 prefix 的子路径
func IsSubPath(path, prefix string) bool {
	// 确保路径以 / 结尾，避免部分匹配问题
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	return strings.HasPrefix(path, prefix)
}

// IsFileInAnyFilterRules 检查文件是否在任一过滤规则中（include 或 download）
func IsFileInAnyFilterRules(filePath, filterRules string) bool {
	if filterRules == "" {
		// 没有过滤规则，返回 false
		return false
	}

	// 解析新格式（包含 include 和 download）
	var rules struct {
		Include  []string `json:"include"`
		Download []string `json:"download"`
	}

	if err := json.Unmarshal([]byte(filterRules), &rules); err != nil {
		// 解析失败，返回 false
		return false
	}

	// 检查 include 规则
	if len(rules.Include) > 0 && checkFileAgainstRules(filePath, rules.Include) {
		return true
	}

	// 检查 download 规则
	if len(rules.Download) > 0 && checkFileAgainstRules(filePath, rules.Download) {
		return true
	}

	return false
}

// IsFileMatchedByFilter 检查文件是否匹配指定类型的过滤规则
// filterType: "include" 或 "download"
func IsFileMatchedByFilter(filePath, filterRules, filterType string) bool {
	if filterRules == "" {
		// 没有过滤规则，默认允许所有文件
		return true
	}

	// 解析新格式（包含 include 和 download）
	var rules struct {
		Include  []string `json:"include"`
		Download []string `json:"download"`
	}

	if err := json.Unmarshal([]byte(filterRules), &rules); err != nil {
		// 解析失败，返回 false
		return false
	}

	var targetRules []string
	switch filterType {
	case "include":
		targetRules = rules.Include
	case "download":
		targetRules = rules.Download
	default:
		return false
	}

	return checkFileAgainstRules(filePath, targetRules)
}

// checkFileAgainstRules 检查文件是否匹配规则列表
func checkFileAgainstRules(filePath string, rules []string) bool {
	if len(rules) == 0 {
		// 空规则，允许所有文件
		return true
	}

	// 获取文件扩展名
	fileExt := strings.ToLower(filepath.Ext(filePath))

	// 检查文件扩展名是否在允许列表中
	for _, rule := range rules {
		rule = strings.ToLower(strings.TrimSpace(rule))
		// 如果规则不是以 . 开头，自动添加 . 前缀
		if rule != "" && !strings.HasPrefix(rule, ".") {
			rule = "." + rule
		}
		if rule == fileExt {
			return true
		}
	}

	return false
}
