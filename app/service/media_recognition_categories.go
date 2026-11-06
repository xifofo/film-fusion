// 分类名称边界校验；分类规则解析和匹配由 medianame 负责。
package service

import (
	"errors"
	"fmt"
	"strings"
)

const maxMediaRecognitionCategoryYAML = 128 << 10

func validateMediaRecognitionCategoryName(name string) error {
	if name == "" {
		return errors.New("分类名不能为空")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("分类名 %q 不能包含路径分隔符或相对路径", name)
	}
	if strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("分类名 %q 包含非法控制字符", name)
	}
	return nil
}

func compactMediaRecognitionValues(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && value != "0" {
			result = append(result, value)
		}
	}
	return result
}
