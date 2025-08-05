package pathhelper

import (
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

func ConvertToLinuxPath(windowsPath string) string {
	// 将所有的反斜杠转换成正斜杠
	linuxPath := strings.ReplaceAll(RemoveDriveLetter(windowsPath), "\\", "/")
	return linuxPath
}
