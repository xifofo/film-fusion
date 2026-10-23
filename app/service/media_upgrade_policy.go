package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xifofo/medianame"
)

// Priority is ordinal. Bonus scores never cross a priority boundary.
type MediaUpgradePolicy struct {
	Enabled          bool                   `json:"enabled"`
	Name             string                 `json:"name"`
	Rules            []MediaUpgradeRule     `json:"rules"`
	Bonuses          []MediaUpgradeRule     `json:"bonuses"`
	Exclude          []string               `json:"exclude"`
	MaxSizeMB        int64                  `json:"max_size_mb"`
	SameTier         bool                   `json:"same_tier"`
	MinScoreGain     int                    `json:"min_score_gain"`
	GroupID          uint                   `json:"group_id,omitempty"`
	GroupRevision    uint                   `json:"group_revision,omitempty"`
	ExcludeCondition *MediaUpgradeCondition `json:"exclude_condition,omitempty"`
}

type MediaUpgradeRule struct {
	Name           string                 `json:"name"`
	Resolutions    []string               `json:"resolutions,omitempty"`
	Sources        []string               `json:"sources,omitempty"`
	Codecs         []string               `json:"codecs,omitempty"`
	Effects        []string               `json:"effects,omitempty"`
	Groups         []string               `json:"groups,omitempty"`
	Include        string                 `json:"include,omitempty"`
	Score          int                    `json:"score,omitempty"`
	AudioCodecs    []string               `json:"audio_codecs,omitempty"`
	AudioChannels  []string               `json:"audio_channels,omitempty"`
	AudioLanguages []string               `json:"audio_languages,omitempty"`
	Subtitles      []string               `json:"subtitles,omitempty"`
	Exclude        string                 `json:"exclude,omitempty"`
	IncludeRegex   string                 `json:"include_regex,omitempty"`
	ExcludeRegex   string                 `json:"exclude_regex,omitempty"`
	Condition      *MediaUpgradeCondition `json:"condition,omitempty"`
}

type MediaUpgradeFacts struct {
	Original       string   `json:"original"`
	Resolution     string   `json:"resolution,omitempty"`
	Source         string   `json:"source,omitempty"`
	Codec          string   `json:"codec,omitempty"`
	Effects        []string `json:"effects,omitempty"`
	Group          string   `json:"group,omitempty"`
	Size           int64    `json:"size"`
	Part           int      `json:"part,omitempty"`
	Cut            string   `json:"cut,omitempty"`
	AudioCodec     string   `json:"audio_codec,omitempty"`
	AudioChannels  string   `json:"audio_channels,omitempty"`
	AudioLanguages []string `json:"audio_languages,omitempty"`
	Subtitles      []string `json:"subtitles,omitempty"`
}

type MediaUpgradeRank struct {
	Tier     int      `json:"tier"`
	Label    string   `json:"label"`
	Score    int      `json:"score"`
	Reasons  []string `json:"reasons"`
	Rejected bool     `json:"rejected,omitempty"`
}

var upgradeCutPattern = regexp.MustCompile(`(?i)(?:^|[ ._\-\[\(])((?:director'?s?[ ._\-]*cut)|extended|theatrical|unrated|uncut|导演剪辑版|加长版|剧场版)(?:$|[ ._\-\]\)])`)

func ParseMediaUpgradeFacts(name string, size int64) MediaUpgradeFacts {
	parsed := medianame.Parse(name)
	facts := MediaUpgradeFacts{Original: name, Resolution: parsed.Resolution, Source: parsed.Source, Codec: parsed.VideoCodec, Effects: parsed.Effects, Group: parsed.ReleaseGroup, Size: size, Part: parsed.Part, AudioCodec: parsed.AudioCodec, AudioChannels: parsed.AudioChannels}
	facts.AudioLanguages, facts.Subtitles = parseUpgradeLanguageTags(name)
	if match := upgradeCutPattern.FindStringSubmatch(name); len(match) > 1 {
		cut := strings.ToLower(strings.NewReplacer(".", "", "_", "", "-", "", " ", "", "'", "").Replace(match[1]))
		switch cut {
		case "directorscut", "directorcut", "导演剪辑版":
			cut = "directors-cut"
		case "加长版":
			cut = "extended"
		case "剧场版":
			cut = "theatrical"
		}
		facts.Cut = cut
	}
	return facts
}

func MediaUpgradePresets() map[string]MediaUpgradePolicy {
	quality := MediaUpgradePolicy{Name: "画质优先", MinScoreGain: 10, Exclude: []string{"CAM", "TS", "DVDScr"}, Bonuses: []MediaUpgradeRule{
		{Name: "修正版", Effects: []string{"PROPER", "REPACK"}, Score: 10},
	}}
	for _, resolution := range []string{"2160p", "1080p", "720p"} {
		for _, source := range []struct {
			label  string
			values []string
		}{{"REMUX", []string{"REMUX"}}, {"蓝光", []string{"BluRay", "BDRip", "BRRip"}}, {"WEB", []string{"WEB-DL", "WEBRip", "WEB"}}, {"HDTV", []string{"HDTV", "UHDTV"}}} {
			quality.Rules = append(quality.Rules, MediaUpgradeRule{Name: resolution + " " + source.label, Resolutions: []string{resolution}, Sources: source.values})
		}
	}
	compatible := MediaUpgradePolicy{Name: "兼容优先", MinScoreGain: 10, Exclude: []string{"CAM", "TS", "DVDScr", "Dolby Vision"}, Bonuses: quality.Bonuses}
	for _, resolution := range []string{"1080p", "720p", "2160p"} {
		compatible.Rules = append(compatible.Rules, MediaUpgradeRule{Name: resolution + " H.264", Resolutions: []string{resolution}, Codecs: []string{"H.264"}})
	}
	space := MediaUpgradePolicy{Name: "节省空间", MinScoreGain: 10, MaxSizeMB: 20480, Exclude: quality.Exclude, Bonuses: quality.Bonuses}
	for _, resolution := range []string{"1080p", "720p", "2160p"} {
		for _, codec := range []string{"H.265", "AV1", "H.264"} {
			space.Rules = append(space.Rules, MediaUpgradeRule{Name: resolution + " " + codec, Resolutions: []string{resolution}, Codecs: []string{codec}})
		}
	}
	return map[string]MediaUpgradePolicy{"quality": quality, "compatible": compatible, "space": space}
}

func ParseMediaUpgradePolicy(raw string) (MediaUpgradePolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return MediaUpgradePolicy{}, nil
	}
	var policy MediaUpgradePolicy
	if len(raw) > 64*1024 {
		return policy, errors.New("洗版策略超过 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("洗版策略格式错误: %w", err)
	}
	// json.Valid also rejects a second JSON value or trailing non-whitespace.
	if !json.Valid([]byte(raw)) {
		return policy, errors.New("洗版策略不是有效的 JSON")
	}
	return policy, ValidateMediaUpgradePolicy(policy)
}

func ValidateMediaUpgradePolicy(policy MediaUpgradePolicy) error {
	if policy.GroupRevision > 0 && policy.GroupID == 0 {
		return errors.New("规则组快照缺少规则组 ID")
	}
	if policy.GroupRevision > 0 && len(policy.Rules) == 0 {
		return errors.New("规则组快照缺少优先级规则")
	}
	if policy.GroupID > 0 && policy.GroupRevision == 0 && (len(policy.Rules) > 0 || len(policy.Bonuses) > 0 || len(policy.Exclude) > 0 || policy.ExcludeCondition != nil || policy.MaxSizeMB != 0 || policy.SameTier || policy.MinScoreGain != 0) {
		return errors.New("规则组引用不能混用内联规则；请使用规则组或目录独立策略")
	}
	if policy.Enabled && len(policy.Rules) == 0 && !(policy.GroupID > 0 && policy.GroupRevision == 0) {
		return errors.New("开启洗版必须配置至少一个优先级档位")
	}
	if len([]rune(policy.Name)) > 120 || len(policy.Rules) > 50 || len(policy.Bonuses) > 30 || len(policy.Exclude) > 30 || policy.MinScoreGain < 0 || policy.MinScoreGain > 10000 || policy.MaxSizeMB < 0 || policy.MaxSizeMB > 100*1024*1024 {
		return errors.New("洗版策略数量或数值超出范围")
	}
	if err := validateUpgradeCondition(policy.ExcludeCondition); err != nil {
		return fmt.Errorf("全局排除条件: %w", err)
	}
	if err := validateUpgradeValues(policy.Exclude, 120); err != nil {
		return fmt.Errorf("全局排除: %w", err)
	}
	for _, rule := range append(append([]MediaUpgradeRule{}, policy.Rules...), policy.Bonuses...) {
		if strings.TrimSpace(rule.Name) == "" || len(rule.Name) > 120 || len(rule.Include) > 200 || rule.Score < -1000 || rule.Score > 1000 {
			return errors.New("洗版规则需要名称，权重范围为 -1000 到 1000")
		}
		if len(rule.Resolutions)+len(rule.Sources)+len(rule.Codecs)+len(rule.Effects)+len(rule.Groups)+len(rule.AudioCodecs)+len(rule.AudioChannels)+len(rule.AudioLanguages)+len(rule.Subtitles) == 0 && strings.TrimSpace(rule.Include) == "" && rule.Exclude == "" && rule.IncludeRegex == "" && rule.ExcludeRegex == "" && rule.Condition == nil {
			return fmt.Errorf("规则 %s 没有匹配条件", rule.Name)
		}
		for _, values := range [][]string{rule.Resolutions, rule.Sources, rule.Codecs, rule.Effects, rule.Groups, rule.AudioCodecs, rule.AudioChannels, rule.AudioLanguages, rule.Subtitles} {
			if err := validateUpgradeValues(values, 120); err != nil {
				return err
			}
		}
		if len(rule.Exclude) > 200 || len(rule.IncludeRegex) > 200 || len(rule.ExcludeRegex) > 200 {
			return errors.New("名称包含、排除和正则最多 200 字节")
		}
		for _, pattern := range []string{rule.IncludeRegex, rule.ExcludeRegex} {
			if pattern != "" {
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("规则 %s 的正则无效: %w", rule.Name, err)
				}
			}
		}
		if err := validateUpgradeCondition(rule.Condition); err != nil {
			return fmt.Errorf("规则 %s: %w", rule.Name, err)
		}
	}
	return nil
}

func upgradeContains(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}

func upgradeRuleMatches(rule MediaUpgradeRule, facts MediaUpgradeFacts) bool {
	return upgradeRuleMatchResult(rule, facts) == upgradeMatchTrue
}

func upgradeLegacyRuleMatches(rule MediaUpgradeRule, facts MediaUpgradeFacts) bool {
	for _, field := range []struct {
		choices []string
		value   string
	}{{rule.Resolutions, facts.Resolution}, {rule.Sources, facts.Source}, {rule.Codecs, facts.Codec}, {rule.Groups, facts.Group}} {
		if len(field.choices) > 0 && !upgradeContains(field.choices, field.value) {
			return false
		}
	}
	if len(rule.Effects) > 0 {
		found := false
		for _, effect := range facts.Effects {
			found = found || upgradeContains(rule.Effects, effect)
		}
		if !found {
			return false
		}
	}
	return rule.Include == "" || strings.Contains(strings.ToLower(facts.Original), strings.ToLower(rule.Include))
}

func RankMediaUpgrade(policy MediaUpgradePolicy, facts MediaUpgradeFacts) MediaUpgradeRank {
	if upgradePolicyNeedsExtendedFacts(policy) {
		facts = EnrichMediaUpgradeFacts(facts)
	}
	rank := MediaUpgradeRank{Reasons: []string{}}
	if policy.GroupID > 0 && policy.GroupRevision == 0 {
		rank.Label = "规则组尚未解析，请重新预览"
		return rank
	}
	if policy.MaxSizeMB > 0 && facts.Size <= 0 {
		rank.Label = "文件大小未知，无法核验大小限制"
		return rank
	}
	if facts.Size > 0 && policy.MaxSizeMB > 0 && facts.Size > policy.MaxSizeMB*1024*1024 {
		rank.Rejected, rank.Label = true, "超过单文件大小限制"
		return rank
	}
	for _, value := range append([]string{facts.Source, facts.Codec}, facts.Effects...) {
		if value != "" && upgradeContains(policy.Exclude, value) {
			rank.Rejected, rank.Label = true, "排除 "+value
			return rank
		}
	}
	if policy.ExcludeCondition != nil {
		switch evaluateUpgradeCondition(policy.ExcludeCondition, facts) {
		case upgradeMatchTrue:
			rank.Rejected, rank.Label = true, "命中全局组合排除条件"
			return rank
		case upgradeMatchUnknown:
			rank.Label = "全局排除条件所需标签未知，待确认"
			return rank
		}
	}
	for i, rule := range policy.Rules {
		matched := upgradeRuleMatchResult(rule, facts)
		if matched == upgradeMatchUnknown {
			rank.Label = "档位 " + rule.Name + " 所需标签未知，待确认"
			return rank
		}
		if matched == upgradeMatchTrue {
			rank.Tier, rank.Label = i+1, rule.Name
			break
		}
	}
	if rank.Tier == 0 {
		rank.Label = "没有匹配档位（质量信息不足或不符合规则）"
		return rank
	}
	for _, rule := range policy.Bonuses {
		matched := upgradeRuleMatchResult(rule, facts)
		if matched == upgradeMatchUnknown && policy.SameTier {
			rank.Tier, rank.Label = 0, "同档偏好 "+rule.Name+" 所需标签未知，待确认"
			return rank
		}
		if matched == upgradeMatchTrue {
			rank.Score += rule.Score
			rank.Reasons = append(rank.Reasons, fmt.Sprintf("%s %+d", rule.Name, rule.Score))
		}
	}
	return rank
}

// CompareMediaUpgrade returns an explainable action. Unknown old quality never
// authorizes replacement. The caller must separately validate media identity.
func CompareMediaUpgrade(policy MediaUpgradePolicy, incoming, existing MediaUpgradeFacts) (string, string) {
	newRank, oldRank := RankMediaUpgrade(policy, incoming), RankMediaUpgrade(policy, existing)
	if newRank.Rejected {
		return "keep", newRank.Label
	}
	if newRank.Tier == 0 || oldRank.Tier == 0 {
		return "review", "新旧版本的质量信息不足，无法确定提升"
	}
	if newRank.Tier < oldRank.Tier {
		return "replace", fmt.Sprintf("%s → %s，优先级提升", oldRank.Label, newRank.Label)
	}
	if newRank.Tier > oldRank.Tier {
		return "keep", "已有版本的优先级更高"
	}
	minimum := policy.MinScoreGain
	if minimum < 1 {
		minimum = 1
	}
	if policy.SameTier && newRank.Score-oldRank.Score >= minimum {
		return "replace", fmt.Sprintf("同档偏好提升 %d 分", newRank.Score-oldRank.Score)
	}
	return "keep", "同档位保留已有版本"
}
