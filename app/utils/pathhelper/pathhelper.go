package pathhelper

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var ErrUnsafePath = errors.New("unsafe path")

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
	return strings.ReplaceAll(RemoveDriveLetter(windowsPath), "\\", "/")
}

func ConvertToWindowsPath(path string) string {
	return strings.ReplaceAll(path, "/", "\\")
}

func SafeFilePathJoin(basePath, relativePath string) string {
	// 检查是否为URL，如果是URL则使用字符串拼接
	if strings.HasPrefix(basePath, "http://") || strings.HasPrefix(basePath, "https://") {
		// 确保basePath以/结尾
		if !strings.HasSuffix(basePath, "/") {
			basePath += "/"
		}
		// 移除relativePath开头的/
		relativePath = strings.TrimPrefix(relativePath, "/")
		return basePath + relativePath
	}

	if runtime.GOOS == "windows" {
		// Windows 系统：使用反斜杠分隔符
		basePath = ConvertToWindowsPath(basePath)
		relativePath = ConvertToWindowsPath(relativePath)
	} else {
		// Unix-like 系统：使用正斜杠分隔符
		basePath = ConvertToLinuxPath(basePath)
		relativePath = ConvertToLinuxPath(relativePath)
	}

	// 使用 filepath.Join 进行路径拼接
	return filepath.Join(basePath, relativePath)
}

// NormalizeUntrustedPath 规范化外部传入的逻辑路径，并拒绝路径穿越。
// CloudDrive2 可能上报 Windows 路径，因此统一转换为 / 后再校验。
func NormalizeUntrustedPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("%w: NUL byte", ErrUnsafePath)
	}

	converted := ConvertToLinuxPath(raw)
	for _, segment := range strings.Split(converted, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: parent traversal", ErrUnsafePath)
		}
	}

	normalized := path.Clean("/" + strings.TrimLeft(converted, "/"))
	return normalized, nil
}

// SafeRelativePath 将外部逻辑路径转换为可交给 os.Root 的相对路径。
func SafeRelativePath(raw string) (string, error) {
	normalized, err := NormalizeUntrustedPath(raw)
	if err != nil {
		return "", err
	}
	relative := strings.TrimPrefix(normalized, "/")
	if relative == "" {
		return ".", nil
	}
	return filepath.FromSlash(relative), nil
}

// JoinUnderRoot 在可信根目录下拼接外部路径，并再次验证结果没有越界。
func JoinUnderRoot(root, raw string) (string, error) {
	relative, err := SafeRelativePath(raw)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootAbs, relative)
	check, err := filepath.Rel(rootAbs, target)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: target escapes root", ErrUnsafePath)
	}
	return target, nil
}

// IsSubPath 检查 candidate 是否位于 prefix 内（包含 prefix 本身）。
func IsSubPath(candidate, prefix string) bool {
	candidatePath, err := NormalizeUntrustedPath(candidate)
	if err != nil {
		return false
	}
	prefixPath, err := NormalizeUntrustedPath(prefix)
	if err != nil {
		return false
	}
	if prefixPath == "/" {
		return true
	}
	return candidatePath == prefixPath || strings.HasPrefix(candidatePath, prefixPath+"/")
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
