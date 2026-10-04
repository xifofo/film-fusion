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
	Enabled      bool               `json:"enabled"`
	Name         string             `json:"name"`
	Rules        []MediaUpgradeRule `json:"rules"`
	Bonuses      []MediaUpgradeRule `json:"bonuses"`
	Exclude      []string           `json:"exclude"`
	MaxSizeMB    int64              `json:"max_size_mb"`
	SameTier     bool               `json:"same_tier"`
	MinScoreGain int                `json:"min_score_gain"`
}

type MediaUpgradeRule struct {
	Name        string   `json:"name"`
	Resolutions []string `json:"resolutions,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Codecs      []string `json:"codecs,omitempty"`
	Effects     []string `json:"effects,omitempty"`
	Groups      []string `json:"groups,omitempty"`
	Include     string   `json:"include,omitempty"`
	Score       int      `json:"score,omitempty"`
}

type MediaUpgradeFacts struct {
	Original   string   `json:"original"`
	Resolution string   `json:"resolution,omitempty"`
	Source     string   `json:"source,omitempty"`
	Codec      string   `json:"codec,omitempty"`
	Effects    []string `json:"effects,omitempty"`
	Group      string   `json:"group,omitempty"`
	Size       int64    `json:"size"`
	Part       int      `json:"part,omitempty"`
	Cut        string   `json:"cut,omitempty"`
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
	facts := MediaUpgradeFacts{Original: name, Resolution: parsed.Resolution, Source: parsed.Source, Codec: parsed.VideoCodec, Effects: parsed.Effects, Group: parsed.ReleaseGroup, Size: size, Part: parsed.Part}
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
	if policy.Enabled && len(policy.Rules) == 0 {
		return errors.New("开启洗版必须配置至少一个优先级档位")
	}
	if len(policy.Name) > 120 || len(policy.Rules) > 50 || len(policy.Bonuses) > 30 || len(policy.Exclude) > 30 || policy.MinScoreGain < 0 || policy.MinScoreGain > 10000 || policy.MaxSizeMB < 0 || policy.MaxSizeMB > 100*1024*1024 {
		return errors.New("洗版策略数量或数值超出范围")
	}
	for _, rule := range append(append([]MediaUpgradeRule{}, policy.Rules...), policy.Bonuses...) {
		if strings.TrimSpace(rule.Name) == "" || len(rule.Name) > 120 || len(rule.Include) > 200 || rule.Score < -1000 || rule.Score > 1000 {
			return errors.New("洗版规则需要名称，权重范围为 -1000 到 1000")
		}
		if len(rule.Resolutions)+len(rule.Sources)+len(rule.Codecs)+len(rule.Effects)+len(rule.Groups) == 0 && strings.TrimSpace(rule.Include) == "" {
			return fmt.Errorf("规则 %s 没有匹配条件", rule.Name)
		}
		for _, values := range [][]string{rule.Resolutions, rule.Sources, rule.Codecs, rule.Effects, rule.Groups, policy.Exclude} {
			if len(values) > 30 {
				return errors.New("单个匹配条件最多 30 项")
			}
			for _, value := range values {
				if strings.TrimSpace(value) == "" || len(value) > 120 {
					return errors.New("匹配条件不能为空或超过 120 字节")
				}
			}
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
	rank := MediaUpgradeRank{Reasons: []string{}}
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
	for i, rule := range policy.Rules {
		if upgradeRuleMatches(rule, facts) {
			rank.Tier, rank.Label = i+1, rule.Name
			break
		}
	}
	if rank.Tier == 0 {
		rank.Label = "没有匹配档位（质量信息不足或不符合规则）"
		return rank
	}
	for _, rule := range policy.Bonuses {
		if upgradeRuleMatches(rule, facts) {
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
