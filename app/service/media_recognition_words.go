package service

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dlclark/regexp2/v2"
)

const (
	maxMediaRecognitionWords      = 500
	maxMediaRecognitionWordBytes  = 4096
	maxMediaRecognitionWordsBytes = 256 << 10
	mediaRecognitionRegexTimeout  = 150 * time.Millisecond
	mediaRecognitionCommentPrefix = "#"
)

// MediaRecognitionRuleType 表示一条识别词的执行方式。
type MediaRecognitionRuleType string

const (
	MediaRecognitionRuleBlock            MediaRecognitionRuleType = "block"
	MediaRecognitionRuleReplace          MediaRecognitionRuleType = "replace"
	MediaRecognitionRuleEpisodeOffset    MediaRecognitionRuleType = "episode_offset"
	MediaRecognitionRuleReplaceAndOffset MediaRecognitionRuleType = "replace_and_offset"
	MediaRecognitionRuleComment          MediaRecognitionRuleType = "comment"
)

// MediaRecognitionRule 是识别词列表中一行规则的结构化说明。
type MediaRecognitionRule struct {
	Line        int                      `json:"line"`
	Raw         string                   `json:"raw"`
	Type        MediaRecognitionRuleType `json:"type"`
	TypeLabel   string                   `json:"type_label"`
	Valid       bool                     `json:"valid"`
	Error       string                   `json:"error,omitempty"`
	Pattern     string                   `json:"pattern,omitempty"`
	Replacement string                   `json:"replacement,omitempty"`
	Front       string                   `json:"front,omitempty"`
	Back        string                   `json:"back,omitempty"`
	Offset      string                   `json:"offset,omitempty"`
}

// MediaRecognitionWordStep 记录一条规则对输入文本的实际影响。
type MediaRecognitionWordStep struct {
	Line    int                      `json:"line"`
	Rule    string                   `json:"rule"`
	Type    MediaRecognitionRuleType `json:"type"`
	Before  string                   `json:"before"`
	After   string                   `json:"after"`
	Applied bool                     `json:"applied"`
	Error   string                   `json:"error,omitempty"`
}

// MediaRecognitionWordResult 是识别词顺序执行后的可观测结果。
type MediaRecognitionWordResult struct {
	Original     string                     `json:"original"`
	Processed    string                     `json:"processed"`
	AppliedWords []string                   `json:"applied_words"`
	Steps        []MediaRecognitionWordStep `json:"steps"`
}

// NormalizeMediaRecognitionWords 清理来自文本框或数据库的识别词行。
func NormalizeMediaRecognitionWords(words []string) []string {
	normalized := make([]string, 0, len(words))
	for _, word := range words {
		word = strings.TrimSuffix(strings.ReplaceAll(word, "\r", ""), "\n")
		word = strings.TrimRightFunc(word, unicode.IsSpace)
		if strings.HasSuffix(word, " =>") {
			// MoviePilot 依赖运算符后的空格识别空替换，持久化时补回被编辑器裁掉的空格。
			word += " "
		}
		if strings.TrimSpace(word) == "" {
			continue
		}
		normalized = append(normalized, word)
	}
	return normalized
}

// ParseMediaRecognitionRules 将识别词文本解析成可展示、可验证的规则列表。
func ParseMediaRecognitionRules(words []string) []MediaRecognitionRule {
	normalized := NormalizeMediaRecognitionWords(words)
	rules := make([]MediaRecognitionRule, 0, len(normalized))
	for index, raw := range normalized {
		rules = append(rules, parseMediaRecognitionRule(index+1, raw))
	}
	return rules
}

// ValidateMediaRecognitionWords 校验识别词数量、大小、格式、正则和偏移表达式。
func ValidateMediaRecognitionWords(words []string) error {
	normalized := NormalizeMediaRecognitionWords(words)
	if len(normalized) > maxMediaRecognitionWords {
		return fmt.Errorf("识别词不能超过 %d 条", maxMediaRecognitionWords)
	}
	totalBytes := 0
	for index, word := range normalized {
		totalBytes += len(word)
		if len(word) > maxMediaRecognitionWordBytes {
			return fmt.Errorf("第 %d 条识别词不能超过 %d 字节", index+1, maxMediaRecognitionWordBytes)
		}
	}
	if totalBytes > maxMediaRecognitionWordsBytes {
		return fmt.Errorf("识别词总大小不能超过 %d KiB", maxMediaRecognitionWordsBytes>>10)
	}
	for _, rule := range ParseMediaRecognitionRules(normalized) {
		if !rule.Valid {
			return fmt.Errorf("第 %d 行无效: %s", rule.Line, rule.Error)
		}
	}
	return nil
}

// ApplyMediaRecognitionWords 按列表顺序执行识别词，并保留逐条测试轨迹。
func ApplyMediaRecognitionWords(input string, words []string) MediaRecognitionWordResult {
	result := MediaRecognitionWordResult{
		Original:     input,
		Processed:    input,
		AppliedWords: make([]string, 0),
		Steps:        make([]MediaRecognitionWordStep, 0),
	}
	for _, rule := range ParseMediaRecognitionRules(words) {
		if rule.Type == MediaRecognitionRuleComment {
			continue
		}
		step := MediaRecognitionWordStep{
			Line: rule.Line, Rule: rule.Raw, Type: rule.Type,
			Before: result.Processed, After: result.Processed,
		}
		if !rule.Valid {
			step.Error = rule.Error
			result.Steps = append(result.Steps, step)
			continue
		}

		updated, applied, err := applyMediaRecognitionRule(result.Processed, rule)
		if err != nil {
			step.Error = err.Error()
		} else {
			step.Applied = applied
			step.After = updated
			// MoviePilot 的组合规则会保留已经完成的替换，即使后续没有
			// 定位到可偏移的集数；此时不会记录为命中，但标题仍应更新。
			if updated != result.Processed {
				result.Processed = updated
			}
			if applied {
				result.AppliedWords = append(result.AppliedWords, rule.Raw)
			}
		}
		result.Steps = append(result.Steps, step)
	}
	return result
}

func parseMediaRecognitionRule(line int, raw string) MediaRecognitionRule {
	rule := MediaRecognitionRule{Line: line, Raw: raw, Valid: true}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, mediaRecognitionCommentPrefix) {
		rule.Type = MediaRecognitionRuleComment
		rule.TypeLabel = "注释"
		return rule
	}

	if strings.Contains(raw, " && ") {
		parts := strings.SplitN(raw, " && ", 2)
		if len(parts) != 2 {
			return invalidMediaRecognitionRule(rule, "组合规则格式应为：替换规则 && 集偏移规则")
		}
		left, leftOK := parseReplacementRule(parts[0])
		right, rightOK := parseEpisodeOffsetRule(parts[1])
		if !leftOK || !rightOK {
			return invalidMediaRecognitionRule(rule, "组合规则格式应为：被替换词 => 替换词 && 前定位词 <> 后定位词 >> EP+偏移量")
		}
		rule.Type = MediaRecognitionRuleReplaceAndOffset
		rule.TypeLabel = "替换并偏移"
		rule.Pattern, rule.Replacement = left[0], left[1]
		rule.Front, rule.Back, rule.Offset = right[0], right[1], right[2]
		return validateParsedMediaRecognitionRule(rule)
	}

	if replacement, ok := parseReplacementRule(raw); ok {
		rule.Type = MediaRecognitionRuleReplace
		rule.TypeLabel = "替换词"
		rule.Pattern, rule.Replacement = replacement[0], replacement[1]
		return validateParsedMediaRecognitionRule(rule)
	}
	if offset, ok := parseEpisodeOffsetRule(raw); ok {
		rule.Type = MediaRecognitionRuleEpisodeOffset
		rule.TypeLabel = "集偏移"
		rule.Front, rule.Back, rule.Offset = offset[0], offset[1], offset[2]
		return validateParsedMediaRecognitionRule(rule)
	}
	if strings.Contains(raw, "=>") || strings.Contains(raw, "<>") || strings.Contains(raw, ">>") {
		return invalidMediaRecognitionRule(rule, "运算符两侧必须保留空格，例如：旧词 => 新词")
	}

	rule.Type = MediaRecognitionRuleBlock
	rule.TypeLabel = "屏蔽词"
	rule.Pattern = raw
	return validateParsedMediaRecognitionRule(rule)
}

func parseReplacementRule(raw string) ([2]string, bool) {
	parts := strings.SplitN(raw, " => ", 2)
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
		return [2]string{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])}, true
	}
	// 行尾空白通常会被编辑器或 JSON 客户端裁掉，因此同时接受
	// "被替换词 =>" 来表达替换为空；无前置空格的 "被替换词=>" 仍会拒绝。
	if strings.HasSuffix(raw, " =>") {
		pattern := strings.TrimSpace(strings.TrimSuffix(raw, " =>"))
		if pattern != "" {
			return [2]string{pattern, ""}, true
		}
	}
	return [2]string{}, false
}

func parseEpisodeOffsetRule(raw string) ([3]string, bool) {
	locations := strings.SplitN(raw, " <> ", 2)
	if len(locations) != 2 {
		return [3]string{}, false
	}
	offset := strings.SplitN(locations[1], " >> ", 2)
	if len(offset) != 2 || strings.TrimSpace(offset[1]) == "" {
		return [3]string{}, false
	}
	front := strings.TrimSpace(locations[0])
	back := strings.TrimSpace(offset[0])
	if front == "" && back == "" {
		return [3]string{}, false
	}
	return [3]string{front, back, strings.TrimSpace(offset[1])}, true
}

func invalidMediaRecognitionRule(rule MediaRecognitionRule, message string) MediaRecognitionRule {
	rule.Valid = false
	rule.Error = message
	if rule.TypeLabel == "" {
		rule.TypeLabel = "格式错误"
	}
	return rule
}

func validateParsedMediaRecognitionRule(rule MediaRecognitionRule) MediaRecognitionRule {
	for _, pattern := range []string{rule.Pattern, rule.Front, rule.Back} {
		if pattern == "" {
			continue
		}
		if _, err := compileMediaRecognitionRegex(pattern); err != nil {
			return invalidMediaRecognitionRule(rule, "正则表达式无效: "+err.Error())
		}
	}
	if rule.Offset != "" {
		if _, err := calculateMediaRecognitionEpisode(rule.Offset, 1); err != nil {
			return invalidMediaRecognitionRule(rule, "集偏移表达式无效: "+err.Error())
		}
	}
	return rule
}

func compileMediaRecognitionRegex(pattern string) (*regexp2.Regexp, error) {
	re, err := regexp2.Compile(
		pattern,
		regexp2.RE2,
		regexp2.OptionMaxBacktrackingStackSize(100_000),
		regexp2.OptionMaxCachedRuneBufferLength(16_384),
	)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = mediaRecognitionRegexTimeout
	return re, nil
}

func applyMediaRecognitionRule(input string, rule MediaRecognitionRule) (string, bool, error) {
	switch rule.Type {
	case MediaRecognitionRuleBlock:
		return replaceMediaRecognitionRegex(input, rule.Pattern, "")
	case MediaRecognitionRuleReplace:
		return replaceMediaRecognitionRegex(input, rule.Pattern, rule.Replacement)
	case MediaRecognitionRuleEpisodeOffset:
		return applyMediaRecognitionEpisodeOffset(input, rule.Front, rule.Back, rule.Offset)
	case MediaRecognitionRuleReplaceAndOffset:
		replaced, applied, err := replaceMediaRecognitionRegex(input, rule.Pattern, rule.Replacement)
		if err != nil || !applied {
			return input, false, err
		}
		offset, offsetApplied, err := applyMediaRecognitionEpisodeOffset(replaced, rule.Front, rule.Back, rule.Offset)
		if err != nil {
			return replaced, true, err
		}
		if !offsetApplied {
			return replaced, false, nil
		}
		return offset, true, nil
	default:
		return input, false, nil
	}
}

func replaceMediaRecognitionRegex(input, pattern, replacement string) (string, bool, error) {
	re, err := compileMediaRecognitionRegex(pattern)
	if err != nil {
		return input, false, err
	}
	matched, err := re.MatchString(input)
	if err != nil || !matched {
		return input, false, err
	}
	updated, err := re.Replace(input, normalizeMediaRecognitionReplacement(replacement), 0, -1)
	if err != nil {
		return input, false, err
	}
	return updated, true, nil
}

var (
	pythonNumberedBackReference = regexp.MustCompile(`\\([1-9][0-9]*)`)
	pythonNamedBackReference    = regexp.MustCompile(`\\g<([A-Za-z_][A-Za-z0-9_]*|[0-9]+)>`)
	mediaRecognitionEpisodeRE   = regexp.MustCompile(`[0-9零一二两三四五六七八九十百]+`)
)

func normalizeMediaRecognitionReplacement(replacement string) string {
	replacement = pythonNamedBackReference.ReplaceAllString(replacement, `${$1}`)
	return pythonNumberedBackReference.ReplaceAllString(replacement, `$$$1`)
}

type mediaRecognitionEpisodeReplacement struct {
	start int
	end   int
	value string
}

func applyMediaRecognitionEpisodeOffset(input, front, back, expression string) (string, bool, error) {
	runes := []rune(input)
	regions, err := locateMediaRecognitionEpisodeRegions(input, front, back)
	if err != nil {
		return input, false, err
	}
	replacements := make([]mediaRecognitionEpisodeReplacement, 0)
	seen := make(map[[2]int]struct{})
	for _, region := range regions {
		if region[0] < 0 || region[1] < region[0] || region[1] > len(runes) {
			continue
		}
		segment := string(runes[region[0]:region[1]])
		for _, indexes := range mediaRecognitionEpisodeRE.FindAllStringIndex(segment, -1) {
			prefixRunes := []rune(segment[:indexes[0]])
			valueRunes := []rune(segment[indexes[0]:indexes[1]])
			start := region[0] + len(prefixRunes)
			end := start + len(valueRunes)
			key := [2]int{start, end}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			original := string(valueRunes)
			episode, numeric, err := parseMediaRecognitionEpisode(original)
			if err != nil {
				return input, false, err
			}
			shifted, err := calculateMediaRecognitionEpisode(expression, episode)
			if err != nil {
				return input, false, err
			}
			replacements = append(replacements, mediaRecognitionEpisodeReplacement{
				start: start, end: end,
				value: formatMediaRecognitionEpisode(original, shifted, numeric),
			})
		}
	}
	if len(replacements) == 0 {
		return input, false, nil
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start > replacements[j].start })
	for _, replacement := range replacements {
		runes = append(
			append(append([]rune{}, runes[:replacement.start]...), []rune(replacement.value)...),
			runes[replacement.end:]...,
		)
	}
	return string(runes), true, nil
}

func locateMediaRecognitionEpisodeRegions(input, front, back string) ([][2]int, error) {
	runeLength := len([]rune(input))
	frontMatches := [][2]int{{0, 0}}
	if front != "" {
		matches, err := findAllMediaRecognitionMatches(input, front)
		if err != nil {
			return nil, err
		}
		frontMatches = matches
	}
	if len(frontMatches) == 0 {
		return nil, nil
	}

	var backRE *regexp2.Regexp
	var err error
	if back != "" {
		backRE, err = compileMediaRecognitionRegex(back)
		if err != nil {
			return nil, err
		}
	}
	regions := make([][2]int, 0, len(frontMatches))
	for _, match := range frontMatches {
		start := match[1]
		end := runeLength
		if backRE != nil {
			tail := string([]rune(input)[start:])
			backMatch, matchErr := backRE.FindStringMatch(tail)
			if matchErr != nil {
				return nil, matchErr
			}
			if backMatch == nil {
				continue
			}
			end = start + backMatch.RuneIndex
		}
		if start <= end {
			regions = append(regions, [2]int{start, end})
		}
	}
	return regions, nil
}

func findAllMediaRecognitionMatches(input, pattern string) ([][2]int, error) {
	re, err := compileMediaRecognitionRegex(pattern)
	if err != nil {
		return nil, err
	}
	match, err := re.FindStringMatch(input)
	if err != nil {
		return nil, err
	}
	matches := make([][2]int, 0)
	for match != nil {
		matches = append(matches, [2]int{match.RuneIndex, match.RuneIndex + match.RuneLength})
		match, err = re.FindNextMatch(match)
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func parseMediaRecognitionEpisode(value string) (int, bool, error) {
	if number, err := strconv.Atoi(value); err == nil {
		return number, true, nil
	}
	number, err := chineseMediaRecognitionNumber(value)
	return number, false, err
}

func chineseMediaRecognitionNumber(value string) (int, error) {
	digits := map[rune]int{'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, section, current := 0, 0, 0
	for _, char := range value {
		if digit, ok := digits[char]; ok {
			current = digit
			continue
		}
		switch char {
		case '十':
			if current == 0 {
				current = 1
			}
			section += current * 10
			current = 0
		case '百':
			if current == 0 {
				current = 1
			}
			section += current * 100
			current = 0
		default:
			return 0, fmt.Errorf("无法识别中文集数 %q", value)
		}
	}
	total += section + current
	return total, nil
}

func formatMediaRecognitionEpisode(original string, value int, numeric bool) string {
	if !numeric {
		return chineseMediaRecognitionNumberText(value)
	}
	width := 0
	unsigned := original
	if strings.HasPrefix(unsigned, "0") {
		width = len(unsigned)
	}
	abs := value
	if abs < 0 {
		abs = -abs
	}
	formatted := strconv.Itoa(abs)
	if width > 0 {
		formatted = fmt.Sprintf("%0*d", width, abs)
	}
	if value < 0 {
		return "-" + formatted
	}
	return formatted
}

func chineseMediaRecognitionNumberText(value int) string {
	if value < 0 {
		return "负" + chineseMediaRecognitionNumberText(-value)
	}
	digits := []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九"}
	if value < 10 {
		return digits[value]
	}
	if value < 100 {
		tens, ones := value/10, value%10
		prefix := "十"
		if tens > 1 {
			prefix = digits[tens] + "十"
		}
		if ones > 0 {
			prefix += digits[ones]
		}
		return prefix
	}
	if value < 1000 {
		hundreds, rest := value/100, value%100
		text := digits[hundreds] + "百"
		if rest == 0 {
			return text
		}
		if rest < 10 {
			text += "零"
		}
		return text + chineseMediaRecognitionNumberText(rest)
	}
	return strconv.Itoa(value)
}

type mediaRecognitionExpressionParser struct {
	input   string
	index   int
	episode float64
}

func calculateMediaRecognitionEpisode(expression string, episode int) (int, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return 0, errors.New("集偏移量不能为空")
	}
	if regexp.MustCompile(`(?i)(?:\d|\))\s*EP|EP\s*(?:\d|\()`).MatchString(expression) {
		return 0, errors.New("EP 前后不能省略运算符")
	}
	parser := &mediaRecognitionExpressionParser{input: expression, episode: float64(episode)}
	value, err := parser.parseExpression()
	if err != nil {
		return 0, err
	}
	parser.skipSpaces()
	if parser.index != len(parser.input) {
		return 0, fmt.Errorf("无法识别 %q", parser.input[parser.index:])
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value > math.MaxInt || value < math.MinInt {
		return 0, errors.New("集偏移结果超出有效范围")
	}
	return int(value), nil
}

func (p *mediaRecognitionExpressionParser) parseExpression() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		if !p.consume("+") && !p.consume("-") {
			return left, nil
		}
		op := p.input[p.index-1]
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}

func (p *mediaRecognitionExpressionParser) parseTerm() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		op := ""
		for _, candidate := range []string{"//", "*", "/", "%"} {
			if p.consume(candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if right == 0 && (op == "/" || op == "//" || op == "%") {
			return 0, errors.New("集偏移表达式不能除以 0")
		}
		switch op {
		case "*":
			left *= right
		case "/":
			left /= right
		case "//":
			left = math.Floor(left / right)
		case "%":
			left = math.Mod(left, right)
			if left != 0 && (left < 0) != (right < 0) {
				// Python（MoviePilot）的余数与除数同号。
				left += right
			}
		}
	}
}

func (p *mediaRecognitionExpressionParser) parseUnary() (float64, error) {
	p.skipSpaces()
	if p.consume("+") {
		return p.parseUnary()
	}
	if p.consume("-") {
		value, err := p.parseUnary()
		return -value, err
	}
	return p.parsePrimary()
}

func (p *mediaRecognitionExpressionParser) parsePrimary() (float64, error) {
	p.skipSpaces()
	if p.consume("(") {
		value, err := p.parseExpression()
		if err != nil {
			return 0, err
		}
		p.skipSpaces()
		if !p.consume(")") {
			return 0, errors.New("集偏移表达式缺少右括号")
		}
		return value, nil
	}
	if strings.HasPrefix(strings.ToUpper(p.input[p.index:]), "EP") {
		p.index += 2
		return p.episode, nil
	}
	start := p.index
	for p.index < len(p.input) && p.input[p.index] >= '0' && p.input[p.index] <= '9' {
		p.index++
	}
	if start == p.index {
		return 0, errors.New("集偏移表达式仅支持数字、EP、括号和 + - * / // %")
	}
	value, _ := strconv.ParseFloat(p.input[start:p.index], 64)
	return value, nil
}

func (p *mediaRecognitionExpressionParser) skipSpaces() {
	for p.index < len(p.input) && unicode.IsSpace(rune(p.input[p.index])) {
		p.index++
	}
}

func (p *mediaRecognitionExpressionParser) consume(token string) bool {
	if strings.HasPrefix(p.input[p.index:], token) {
		p.index += len(token)
		return true
	}
	return false
}
