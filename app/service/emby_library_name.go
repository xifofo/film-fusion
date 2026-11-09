// 批量查入库的本地名称解析和候选相关性判断，不调用在线媒体识别服务。
package service

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xifofo/medianame"
	"golang.org/x/text/unicode/norm"
)

type embyLibraryQuery struct {
	title  string
	year   int
	series bool
}

var embyLibraryExplicitYear = regexp.MustCompile(`[（(\[]\s*(?:18|19|20)[0-9]{2}\s*[）)\]]`)

func (q embyLibraryQuery) displayName() string {
	if q.year > 0 {
		return fmt.Sprintf("%s (%d)", q.title, q.year)
	}
	return q.title
}

func cleanEmbyLibraryName(value string) string {
	return parseEmbyLibraryQuery(value).displayName()
}

func parseEmbyLibraryQuery(value string) embyLibraryQuery {
	name := stripEmbyLibraryName(value)
	query := embyLibraryQuery{title: name}
	parsed := medianame.Parse(name)
	if strings.TrimSpace(parsed.Title) == "" || embyLibraryNameKey(parsed.Title) == "" {
		return query
	}

	technical := 0
	for _, field := range []string{parsed.Resolution, parsed.Source, parsed.VideoCodec, parsed.AudioCodec, parsed.StreamingService} {
		if field != "" {
			technical++
		}
	}
	// 只有明确季集、括号年份或资源标记时才采用解析结果，避免破坏
	// Mr.Robot、Avatar.2、A.2025 等普通片名；单独的分辨率词也可能属于片名。
	hasExtension := embyLibraryHasExtension(value)
	if parsed.Season == nil && parsed.Episode == nil && !embyLibraryExplicitYear.MatchString(name) && technical < 2 && !(hasExtension && technical > 0) {
		return query
	}
	query.title, query.year = parsed.Title, parsed.Year
	// medianame 对没有剧集线索的名称默认推断为电影，此处不据此排除剧集。
	query.series = parsed.Type == medianame.TypeTV
	return query
}

func embyLibraryHasExtension(value string) bool {
	name := strings.Trim(strings.TrimSpace(value), `"'“”‘’`)
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		return embyLibraryExtensions[strings.ToLower(name[dot+1:])]
	}
	return false
}

// 疑似候选必须包含完整片名，且短片名至少覆盖长片名的六成。
// 拉丁文字按完整词匹配，中文按完整片名匹配，不使用公共单词或编辑距离猜测。
func relatedEmbyLibraryTitle(left, right string) bool {
	words := func(value string) string {
		return strings.Join(strings.FieldsFunc(norm.NFKC.String(strings.ToLower(value)), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}), " ")
	}
	a, b := words(left), words(right)
	aKey, bKey := embyLibraryNameKey(a), embyLibraryNameKey(b)
	if utf8.RuneCountInString(aKey) > utf8.RuneCountInString(bKey) {
		a, b, aKey, bKey = b, a, bKey, aKey
	}
	short, long := utf8.RuneCountInString(aKey), utf8.RuneCountInString(bKey)
	hasHan := strings.ContainsFunc(a, func(r rune) bool { return unicode.Is(unicode.Han, r) })
	if short < 2 || (!hasHan && short < 4) || short*5 < long*3 {
		return false
	}
	if strings.Contains(" "+b+" ", " "+a+" ") {
		return true
	}
	// 无空格的中文副标题仍可保留为疑似；混合拉丁片名继续要求完整词边界。
	return hasHan && strings.IndexFunc(aKey, func(r rune) bool { return !unicode.Is(unicode.Han, r) }) == -1 && strings.Contains(bKey, aKey)
}
