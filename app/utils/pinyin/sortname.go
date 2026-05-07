// Package pinyin 提供把媒体标题转换成 Emby SortName（拼音首字母）的工具函数。
//
// 转换规则：
//  1. 剥离前缀冠词 The / A / An（不区分大小写，仅当后面跟空格时才剥离）
//  2. 中文字符 → 拼音首字母大写
//  3. 英文字母 → 原样保留并大写
//  4. 数字 → 原样保留
//  5. 空格与其它符号 → 全部丢弃
//  6. 处理后若以数字开头 → 整体前置 "#"，方便 Emby 字母索引归为 # 组
//  7. 处理后若为空 → 返回 "#"
package pinyin

import (
	"strings"
	"unicode"

	gopinyin "github.com/mozillazg/go-pinyin"
)

var pyArgs = func() gopinyin.Args {
	a := gopinyin.NewArgs()
	a.Style = gopinyin.FirstLetter
	// 多音字只取首选项；首字母模式下差异极小
	a.Heteronym = false
	return a
}()

// ToSortName 把媒体标题转换为 Emby SortName。
// 详见 package 注释里的规则说明。
func ToSortName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "#"
	}

	trimmed = stripLeadingArticle(trimmed)

	var b strings.Builder
	b.Grow(len(trimmed))

	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(unicode.ToUpper(r))
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case isHan(r):
			letters := gopinyin.Pinyin(string(r), pyArgs)
			if len(letters) > 0 && len(letters[0]) > 0 {
				b.WriteString(strings.ToUpper(letters[0][0]))
			}
		default:
			// 空格、标点等其它字符全部丢弃
		}
	}

	out := b.String()
	if out == "" {
		return "#"
	}
	if first := out[0]; first >= '0' && first <= '9' {
		out = "#" + out
	}
	return out
}

// stripLeadingArticle 去掉前缀 "The "/"A "/"An "（不区分大小写）。
func stripLeadingArticle(s string) string {
	lower := strings.ToLower(s)
	for _, prefix := range []string{"the ", "an ", "a "} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(s[len(prefix):])
		}
	}
	return s
}

// isHan 判断 rune 是否为汉字（CJK 统一表意区段）。
func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r)
}
