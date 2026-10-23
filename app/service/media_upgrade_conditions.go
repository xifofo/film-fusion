package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xifofo/medianame"
)

// EnrichMediaUpgradeFacts fills only newly introduced filename facts from the
// preserved original name. Existing quality evidence and explicit facts survive
// unchanged, including evidence persisted before audio/subtitle rules existed.
func EnrichMediaUpgradeFacts(facts MediaUpgradeFacts) MediaUpgradeFacts {
	if strings.TrimSpace(facts.Original) == "" {
		return facts
	}
	if facts.AudioCodec == "" || facts.AudioChannels == "" {
		parsed := medianame.Parse(facts.Original)
		if facts.AudioCodec == "" {
			facts.AudioCodec = parsed.AudioCodec
		}
		if facts.AudioChannels == "" {
			facts.AudioChannels = parsed.AudioChannels
		}
	}
	if len(facts.AudioLanguages) == 0 || len(facts.Subtitles) == 0 {
		audio, subtitles := parseUpgradeLanguageTags(facts.Original)
		if len(facts.AudioLanguages) == 0 {
			facts.AudioLanguages = audio
		}
		if len(facts.Subtitles) == 0 {
			facts.Subtitles = subtitles
		}
	}
	return facts
}

func upgradePolicyNeedsExtendedFacts(policy MediaUpgradePolicy) bool {
	if upgradeConditionNeedsExtendedFacts(policy.ExcludeCondition, 1) {
		return true
	}
	for _, rules := range [][]MediaUpgradeRule{policy.Rules, policy.Bonuses} {
		for _, rule := range rules {
			if len(rule.AudioCodecs)+len(rule.AudioChannels)+len(rule.AudioLanguages)+len(rule.Subtitles) > 0 || upgradeConditionNeedsExtendedFacts(rule.Condition, 1) {
				return true
			}
		}
	}
	return false
}

func upgradeConditionNeedsExtendedFacts(node *MediaUpgradeCondition, depth int) bool {
	if node == nil || depth > maxUpgradeConditionDepth {
		return false
	}
	if strings.HasPrefix(node.Field, "audio_") || node.Field == "subtitle" {
		return true
	}
	for i := range node.Children {
		if upgradeConditionNeedsExtendedFacts(&node.Children[i], depth+1) {
			return true
		}
	}
	return false
}

// MediaUpgradeCondition combines filename-declared facts. Missing facts remain
// unknown, including below NOT; they are never evidence of absence.
type MediaUpgradeCondition struct {
	Op       string                  `json:"op"`
	Children []MediaUpgradeCondition `json:"children,omitempty"`
	Field    string                  `json:"field,omitempty"`
	Values   []string                `json:"values,omitempty"`
	Match    string                  `json:"match,omitempty"`
}

type upgradeMatchResult uint8

const (
	upgradeMatchUnknown upgradeMatchResult = iota
	upgradeMatchFalse
	upgradeMatchTrue
	maxUpgradeConditionDepth = 8
	maxUpgradeConditionNodes = 64
)

var upgradeConditionFields = map[string]struct{}{
	"resolution": {}, "source": {}, "codec": {}, "effect": {}, "group": {}, "name": {},
	"audio_codec": {}, "audio_channels": {}, "audio_language": {}, "subtitle": {},
}

func validateUpgradeValues(values []string, limit int) error {
	if len(values) > 30 {
		return errors.New("单个匹配条件最多 30 项")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > limit {
			return fmt.Errorf("匹配条件不能为空或超过 %d 字节", limit)
		}
	}
	return nil
}

func validateUpgradeCondition(condition *MediaUpgradeCondition) error {
	if condition == nil {
		return nil
	}
	nodes := 0
	var visit func(*MediaUpgradeCondition, int) error
	visit = func(node *MediaUpgradeCondition, depth int) error {
		nodes++
		if depth > maxUpgradeConditionDepth || nodes > maxUpgradeConditionNodes {
			return errors.New("组合条件最多 8 层、64 个节点")
		}
		switch node.Op {
		case "all", "any", "not":
			if node.Field != "" || len(node.Values) != 0 || node.Match != "" {
				return errors.New("组合节点不能同时设置字段或匹配值")
			}
			if len(node.Children) == 0 || (node.Op == "not" && len(node.Children) != 1) {
				return errors.New("all/any 至少需要一个子条件，not 只能有一个子条件")
			}
			for i := range node.Children {
				if err := visit(&node.Children[i], depth+1); err != nil {
					return err
				}
			}
		case "match":
			if len(node.Children) != 0 {
				return errors.New("匹配节点不能包含子条件")
			}
			if _, valid := upgradeConditionFields[node.Field]; !valid {
				return fmt.Errorf("不支持的条件字段 %q", node.Field)
			}
			if len(node.Values) == 0 {
				return errors.New("匹配节点至少需要一个匹配值")
			}
			if node.Match != "" && node.Match != "equals" && node.Match != "contains" && node.Match != "regex" {
				return fmt.Errorf("不支持的匹配方式 %q", node.Match)
			}
			limit := 120
			if node.Match == "regex" {
				limit = 200
			}
			if err := validateUpgradeValues(node.Values, limit); err != nil {
				return err
			}
			if node.Match == "regex" {
				for _, pattern := range node.Values {
					if _, err := regexp.Compile(pattern); err != nil {
						return fmt.Errorf("组合条件正则无效: %w", err)
					}
				}
			}
		default:
			return fmt.Errorf("不支持的条件操作 %q", node.Op)
		}
		return nil
	}
	return visit(condition, 1)
}

func upgradeAll(results ...upgradeMatchResult) upgradeMatchResult {
	result := upgradeMatchTrue
	for _, candidate := range results {
		if candidate == upgradeMatchFalse {
			return upgradeMatchFalse
		}
		if candidate == upgradeMatchUnknown {
			result = upgradeMatchUnknown
		}
	}
	return result
}

func upgradeNot(result upgradeMatchResult) upgradeMatchResult {
	switch result {
	case upgradeMatchTrue:
		return upgradeMatchFalse
	case upgradeMatchFalse:
		return upgradeMatchTrue
	default:
		return upgradeMatchUnknown
	}
}

func upgradeFactsValues(field string, facts MediaUpgradeFacts) []string {
	switch field {
	case "resolution":
		return []string{facts.Resolution}
	case "source":
		return []string{facts.Source}
	case "codec":
		return []string{facts.Codec}
	case "effect":
		return facts.Effects
	case "group":
		return []string{facts.Group}
	case "name":
		return []string{facts.Original}
	case "audio_codec":
		return []string{facts.AudioCodec}
	case "audio_channels":
		return []string{facts.AudioChannels}
	case "audio_language":
		return facts.AudioLanguages
	case "subtitle":
		return facts.Subtitles
	default:
		return nil
	}
}

func upgradeMatchValues(actual, expected []string, mode string) upgradeMatchResult {
	known := false
	for _, value := range actual {
		if strings.TrimSpace(value) == "" {
			continue
		}
		known = true
		for _, choice := range expected {
			switch mode {
			case "", "equals":
				if strings.EqualFold(value, strings.TrimSpace(choice)) {
					return upgradeMatchTrue
				}
			case "contains":
				if strings.Contains(strings.ToLower(value), strings.ToLower(choice)) {
					return upgradeMatchTrue
				}
			case "regex":
				compiled, err := regexp.Compile(choice)
				if err != nil {
					return upgradeMatchUnknown
				}
				if compiled.MatchString(value) {
					return upgradeMatchTrue
				}
			default:
				return upgradeMatchUnknown
			}
		}
	}
	if !known || len(expected) == 0 {
		return upgradeMatchUnknown
	}
	return upgradeMatchFalse
}

func evaluateUpgradeCondition(condition *MediaUpgradeCondition, facts MediaUpgradeFacts) upgradeMatchResult {
	return evaluateUpgradeConditionDepth(condition, facts, 1)
}

func evaluateUpgradeConditionDepth(node *MediaUpgradeCondition, facts MediaUpgradeFacts, depth int) upgradeMatchResult {
	if node == nil || depth > maxUpgradeConditionDepth {
		return upgradeMatchUnknown
	}
	if node.Op == "match" {
		return upgradeMatchValues(upgradeFactsValues(node.Field, facts), node.Values, node.Match)
	}
	if node.Op == "not" {
		if len(node.Children) != 1 {
			return upgradeMatchUnknown
		}
		return upgradeNot(evaluateUpgradeConditionDepth(&node.Children[0], facts, depth+1))
	}
	if len(node.Children) == 0 || (node.Op != "all" && node.Op != "any") {
		return upgradeMatchUnknown
	}
	unknown := false
	for i := range node.Children {
		result := evaluateUpgradeConditionDepth(&node.Children[i], facts, depth+1)
		if node.Op == "all" && result == upgradeMatchFalse {
			return upgradeMatchFalse
		}
		if node.Op == "any" && result == upgradeMatchTrue {
			return upgradeMatchTrue
		}
		unknown = unknown || result == upgradeMatchUnknown
	}
	if unknown {
		return upgradeMatchUnknown
	}
	if node.Op == "all" {
		return upgradeMatchTrue
	}
	return upgradeMatchFalse
}

func upgradeRuleMatchResult(rule MediaUpgradeRule, facts MediaUpgradeFacts) upgradeMatchResult {
	// Preserve all existing scalar-field semantics for policies saved before
	// extended conditions were introduced.
	if !upgradeLegacyRuleMatches(rule, facts) {
		return upgradeMatchFalse
	}
	results := []upgradeMatchResult{upgradeMatchTrue}
	for _, field := range []struct {
		choices []string
		actual  []string
	}{
		{rule.AudioCodecs, []string{facts.AudioCodec}},
		{rule.AudioChannels, []string{facts.AudioChannels}},
		{rule.AudioLanguages, facts.AudioLanguages},
		{rule.Subtitles, facts.Subtitles},
	} {
		if len(field.choices) > 0 {
			results = append(results, upgradeMatchValues(field.actual, field.choices, "equals"))
		}
	}
	if rule.Exclude != "" {
		results = append(results, upgradeNot(upgradeMatchValues([]string{facts.Original}, []string{rule.Exclude}, "contains")))
	}
	if rule.IncludeRegex != "" {
		results = append(results, upgradeMatchValues([]string{facts.Original}, []string{rule.IncludeRegex}, "regex"))
	}
	if rule.ExcludeRegex != "" {
		results = append(results, upgradeNot(upgradeMatchValues([]string{facts.Original}, []string{rule.ExcludeRegex}, "regex")))
	}
	if rule.Condition != nil {
		results = append(results, evaluateUpgradeCondition(rule.Condition, facts))
	}
	return upgradeAll(results...)
}

var upgradeAudioTags = []struct {
	value   string
	pattern *regexp.Regexp
}{
	{"zh", regexp.MustCompile(`(?i)国语|國語|国配|國配|普通话|普通話|(?:^|[ ._\-\[\]()])(?:Mandarin|Chinese[ ._-]+Audio|ZH[ ._-]+Audio|Audio[ ._-]+ZH)(?:$|[ ._\-\[\]()])`)},
	{"yue", regexp.MustCompile(`(?i)粤语|粵語|粤配|粵配|(?:^|[ ._\-\[\]()])(?:Cantonese|YUE[ ._-]+Audio|Audio[ ._-]+YUE)(?:$|[ ._\-\[\]()])`)},
	{"ja", regexp.MustCompile(`(?i)日语|日語|日配|(?:^|[ ._\-\[\]()])(?:Japanese[ ._-]+Audio|(?:JA|JP|JPN)[ ._-]+Audio|Audio[ ._-]+(?:JA|JP|JPN))(?:$|[ ._\-\[\]()])`)},
	{"en", regexp.MustCompile(`(?i)英语|英語|英配|(?:^|[ ._\-\[\]()])(?:English[ ._-]+Audio|ENG?[ ._-]+Audio|Audio[ ._-]+ENG?)(?:$|[ ._\-\[\]()])`)},
	{"ko", regexp.MustCompile(`(?i)韩语|韓語|韩配|韓配|(?:^|[ ._\-\[\]()])(?:Korean[ ._-]+Audio|KOR?[ ._-]+Audio|Audio[ ._-]+KOR?)(?:$|[ ._\-\[\]()])`)},
}

var upgradeSubtitleTags = []struct {
	value   string
	pattern *regexp.Regexp
}{
	{"zh-Hans", regexp.MustCompile(`(?i)简中|簡中|简体|簡體|简繁|簡繁|(?:^|[ ._\-\[\]()&+])(?:CHS|zh-Hans)(?:$|[ ._\-\[\]()&+])`)},
	{"zh-Hant", regexp.MustCompile(`(?i)繁中|繁体|繁體|简繁|簡繁|(?:^|[ ._\-\[\]()&+])(?:CHT|zh-Hant)(?:$|[ ._\-\[\]()&+])`)},
	{"zh", regexp.MustCompile(`(?i)中字|中文字幕|中英|(?:^|[ ._\-\[\]()])(?:Chinese[ ._-]+Subs?|ZH[ ._-]+Subs?|Subs?[ ._-]+ZH)(?:$|[ ._\-\[\]()])`)},
	{"en", regexp.MustCompile(`(?i)英文字幕|英语字幕|英語字幕|中英|(?:^|[ ._\-\[\]()])(?:English[ ._-]+Subs?|ENG?[ ._-]+Subs?|Subs?[ ._-]+ENG?)(?:$|[ ._\-\[\]()])|(?:CHS|CHT)[ .&+_-]+ENG?(?:$|[ ._\-\[\]()])`)},
	{"ja", regexp.MustCompile(`(?i)日文字幕|日语字幕|日語字幕|(?:^|[ ._\-\[\]()])(?:Japanese[ ._-]+Subs?|(?:JA|JP|JPN)[ ._-]+Subs?|Subs?[ ._-]+(?:JA|JP|JPN))(?:$|[ ._\-\[\]()])`)},
	{"ko", regexp.MustCompile(`(?i)韩文字幕|韓文字幕|韩语字幕|韓語字幕|(?:^|[ ._\-\[\]()])(?:Korean[ ._-]+Subs?|KOR?[ ._-]+Subs?|Subs?[ ._-]+KOR?)(?:$|[ ._\-\[\]()])`)},
}

var upgradeSubtitleAudioWords = strings.NewReplacer("英语字幕", "", "英語字幕", "", "日语字幕", "", "日語字幕", "", "韩语字幕", "", "韓語字幕", "", "粤语字幕", "", "粵語字幕", "", "国语字幕", "", "國語字幕", "")

// Language declarations are accepted only in brackets or after a recognizable
// release marker. Words in titles (Johnny English, 疯狂英语, 中英街一号) are not
// audio/subtitle evidence. Chinese declarations must also be complete labels;
// even a bracketed title containing a language word must not become a tag.
var upgradeLanguageMetadataStart = regexp.MustCompile(`(?i)(?:^|[ ._\-\[\]()【】（）])(?:19[0-9]{2}|20[0-9]{2}|S[0-9]{1,2}(?:E[0-9]{1,3})?|[0-9]{1,2}x[0-9]{1,3}|4320p|2160p|1080[pi]|720p|576p|480p|WEB[ ._-]?DL|WEBRIP|BLURAY|REMUX|HDTV|UHDTV|[HX][ ._-]?26[45]|HEVC|AVC|AV1|AAC(?:[ ._-]?[1-8]\.[01])?|DDP(?:[ ._-]?[1-8]\.[01])?|True[ ._-]?HD|DTS(?:[ ._-]?HD)?|FLAC)(?:$|[ ._\-\[\]()【】（）])`)
var upgradeLanguageBrackets = regexp.MustCompile(`[\[【(（]([^\]】)）]*)[\]】)）]`)
var upgradeHanWords = regexp.MustCompile(`[\p{Han}]+`)
var upgradeChineseLanguageDeclaration = regexp.MustCompile(`^(?:(?:国语|國語|国配|國配|普通话|普通話|粤语|粵語|粤配|粵配|日语|日語|日配|英语|英語|英配|韩语|韓語|韩配)(?:配音|音轨|音軌)?|(?:内嵌|內嵌|内封|內封|外挂|外掛)?(?:简中|簡中|繁中|简体|簡體|繁体|繁體|中字|中文字幕|简繁(?:中字|字幕)?|簡繁(?:中字|字幕)?|中英(?:字幕|双字|雙字)|英文字幕|英语字幕|英語字幕|日文字幕|日语字幕|日語字幕|韩文字幕|韓文字幕|韩语字幕|韓語字幕))+$`)

func upgradeLanguageDeclarationText(name string) string {
	var regions []string
	if marker := upgradeLanguageMetadataStart.FindStringIndex(name); marker != nil {
		regions = append(regions, name[marker[0]:])
	}
	for _, bracket := range upgradeLanguageBrackets.FindAllStringSubmatch(name, -1) {
		regions = append(regions, bracket[1])
	}
	text := " " + strings.Join(regions, " ") + " "
	return upgradeHanWords.ReplaceAllStringFunc(text, func(label string) string {
		if upgradeChineseLanguageDeclaration.MatchString(label) {
			return label
		}
		return " "
	})
}

func parseUpgradeLanguageTags(name string) (audioLanguages, subtitles []string) {
	// Only explicit audio declarations count. A title's original language,
	// country, release group, or an unqualified ENG/JPN tag is not evidence.
	declarations := upgradeLanguageDeclarationText(name)
	audioName := upgradeSubtitleAudioWords.Replace(declarations)
	for _, tag := range upgradeAudioTags {
		if tag.pattern.MatchString(audioName) {
			audioLanguages = append(audioLanguages, tag.value)
		}
	}
	for _, tag := range upgradeSubtitleTags {
		if tag.pattern.MatchString(declarations) {
			subtitles = append(subtitles, tag.value)
		}
	}
	if (upgradeContains(subtitles, "zh-Hans") || upgradeContains(subtitles, "zh-Hant")) && !upgradeContains(subtitles, "zh") {
		subtitles = append(subtitles, "zh")
	}
	return audioLanguages, subtitles
}
