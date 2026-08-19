package service

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"film-fusion/app/model"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const maxMediaRecognitionCategoryYAML = 128 << 10

// DefaultMediaRecognitionCategoryYAML 与 MoviePilot 默认 category.yaml 的有效规则保持一致。
// 注释有意保留在原始 YAML 中，页面可直接以配置文件方式编辑。
const DefaultMediaRecognitionCategoryYAML = `# FilmFusion 本地媒体分类配置（兼容 MoviePilot category.yaml）
# movie、tv 为固定一级键；分类按书写顺序匹配，第一个命中即结束。
# 同一分类内的条件需要全部满足，逗号表示任一值，!值表示排除。
# 空规则是兜底分类，建议放在对应分组最后。
movie:
  动画电影:
    genre_ids: '16'
  华语电影:
    original_language: 'zh,cn,bo,za'
  外语电影:

tv:
  国漫:
    genre_ids: '16'
    origin_country: 'CN,TW,HK'
  日番:
    genre_ids: '16'
    origin_country: 'JP'
  纪录片:
    genre_ids: '99'
  儿童:
    genre_ids: '10762'
  综艺:
    genre_ids: '10764,10767'
  国产剧:
    origin_country: 'CN,TW,HK'
  欧美剧:
    origin_country: 'US,FR,GB,DE,ES,IT,NL,PT,RU,UK'
  日韩剧:
    origin_country: 'JP,KP,KR,TH,IN,SG'
  未分类:
`

// MediaRecognitionCategoryCondition 是 category.yaml 中的一项匹配条件。
type MediaRecognitionCategoryCondition struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// MediaRecognitionCategoryRule 保留分类的原始顺序，Fallback 表示空规则。
type MediaRecognitionCategoryRule struct {
	Name       string                              `json:"name"`
	Fallback   bool                                `json:"fallback"`
	Conditions []MediaRecognitionCategoryCondition `json:"conditions"`
}

// MediaRecognitionCategoryConfig 是有序的本地分类配置。
type MediaRecognitionCategoryConfig struct {
	Movie []MediaRecognitionCategoryRule `json:"movie"`
	TV    []MediaRecognitionCategoryRule `json:"tv"`
}

// MediaRecognitionCategoryConfigResult 是分类配置页面使用的读写结果。
type MediaRecognitionCategoryConfigResult struct {
	Configured bool                           `json:"configured"`
	YAML       string                         `json:"yaml"`
	Movie      []MediaRecognitionCategoryRule `json:"movie"`
	TV         []MediaRecognitionCategoryRule `json:"tv"`
	Warnings   []string                       `json:"warnings"`
}

// ParseMediaRecognitionCategoryYAML 校验并解析 MoviePilot 兼容的 category.yaml。
func ParseMediaRecognitionCategoryYAML(source string) (MediaRecognitionCategoryConfig, []string, error) {
	source = normalizeMediaRecognitionCategoryYAML(source)
	if source == "" {
		return MediaRecognitionCategoryConfig{}, nil, errors.New("分类配置不能为空")
	}
	if len(source) > maxMediaRecognitionCategoryYAML {
		return MediaRecognitionCategoryConfig{}, nil, fmt.Errorf("分类配置不能超过 %d KiB", maxMediaRecognitionCategoryYAML>>10)
	}
	if !utf8.ValidString(source) {
		return MediaRecognitionCategoryConfig{}, nil, errors.New("分类配置不是有效的 UTF-8 文本")
	}

	var document yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(source))
	if err := decoder.Decode(&document); err != nil {
		return MediaRecognitionCategoryConfig{}, nil, fmt.Errorf("YAML 语法错误: %w", err)
	}
	var extraDocument yaml.Node
	if err := decoder.Decode(&extraDocument); !errors.Is(err, io.EOF) {
		if err != nil {
			return MediaRecognitionCategoryConfig{}, nil, fmt.Errorf("YAML 语法错误: %w", err)
		}
		return MediaRecognitionCategoryConfig{}, nil, errors.New("分类配置只能包含一个 YAML 文档")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return MediaRecognitionCategoryConfig{}, nil, errors.New("分类配置根节点必须是 movie、tv 组成的对象")
	}

	root := document.Content[0]
	groups := map[string][]MediaRecognitionCategoryRule{}
	seenGroups := map[string]struct{}{}
	for index := 0; index < len(root.Content); index += 2 {
		keyNode, valueNode := root.Content[index], root.Content[index+1]
		key := strings.TrimSpace(keyNode.Value)
		if key != "movie" && key != "tv" {
			return MediaRecognitionCategoryConfig{}, nil, fmt.Errorf("第 %d 行：一级分类只能是 movie 或 tv，当前为 %q", keyNode.Line, key)
		}
		if _, exists := seenGroups[key]; exists {
			return MediaRecognitionCategoryConfig{}, nil, fmt.Errorf("第 %d 行：一级分类 %s 重复", keyNode.Line, key)
		}
		seenGroups[key] = struct{}{}
		rules, err := parseMediaRecognitionCategoryGroup(key, valueNode)
		if err != nil {
			return MediaRecognitionCategoryConfig{}, nil, err
		}
		groups[key] = rules
	}
	for _, group := range []string{"movie", "tv"} {
		if _, exists := seenGroups[group]; !exists {
			return MediaRecognitionCategoryConfig{}, nil, fmt.Errorf("缺少固定一级分类 %s", group)
		}
	}

	config := MediaRecognitionCategoryConfig{Movie: groups["movie"], TV: groups["tv"]}
	warnings := append(categoryFallbackWarnings("movie", config.Movie), categoryFallbackWarnings("tv", config.TV)...)
	return config, warnings, nil
}

func parseMediaRecognitionCategoryGroup(group string, node *yaml.Node) ([]MediaRecognitionCategoryRule, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("第 %d 行：%s 必须是分类对象", node.Line, group)
	}
	rules := make([]MediaRecognitionCategoryRule, 0, len(node.Content)/2)
	seen := map[string]struct{}{}
	for index := 0; index < len(node.Content); index += 2 {
		nameNode, ruleNode := node.Content[index], node.Content[index+1]
		name := strings.TrimSpace(nameNode.Value)
		if err := validateMediaRecognitionCategoryName(name); err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", nameNode.Line, err)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("第 %d 行：分类名 %q 重复", nameNode.Line, name)
		}
		seen[name] = struct{}{}

		rule := MediaRecognitionCategoryRule{Name: name, Conditions: make([]MediaRecognitionCategoryCondition, 0)}
		if isEmptyYAMLNode(ruleNode) {
			rule.Fallback = true
			rules = append(rules, rule)
			continue
		}
		if ruleNode.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("第 %d 行：分类 %q 的规则必须是条件对象或留空", ruleNode.Line, name)
		}
		seenFields := map[string]struct{}{}
		for conditionIndex := 0; conditionIndex < len(ruleNode.Content); conditionIndex += 2 {
			fieldNode, valueNode := ruleNode.Content[conditionIndex], ruleNode.Content[conditionIndex+1]
			field := strings.TrimSpace(fieldNode.Value)
			if field == "" {
				return nil, fmt.Errorf("第 %d 行：分类 %q 存在空条件名", fieldNode.Line, name)
			}
			if _, exists := seenFields[field]; exists {
				return nil, fmt.Errorf("第 %d 行：分类 %q 的条件 %q 重复", fieldNode.Line, name, field)
			}
			seenFields[field] = struct{}{}
			if valueNode.Kind != yaml.ScalarNode || valueNode.Tag == "!!null" {
				return nil, fmt.Errorf("第 %d 行：条件 %q 必须是字符串或数字", valueNode.Line, field)
			}
			value := strings.TrimSpace(valueNode.Value)
			if value == "" {
				continue
			}
			if err := validateMediaRecognitionCategoryCondition(field, value); err != nil {
				return nil, fmt.Errorf("第 %d 行：%w", valueNode.Line, err)
			}
			rule.Conditions = append(rule.Conditions, MediaRecognitionCategoryCondition{Field: field, Value: value})
		}
		rule.Fallback = len(rule.Conditions) == 0
		rules = append(rules, rule)
	}
	return rules, nil
}

func isEmptyYAMLNode(node *yaml.Node) bool {
	return node == nil || (node.Kind == yaml.ScalarNode && node.Tag == "!!null") ||
		(node.Kind == yaml.MappingNode && len(node.Content) == 0)
}

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

func validateMediaRecognitionCategoryCondition(field, value string) error {
	if strings.ContainsAny(field, "\x00\r\n") {
		return fmt.Errorf("条件名 %q 包含非法控制字符", field)
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item), "!"))
		if item == "" {
			return fmt.Errorf("条件 %q 中存在空值", field)
		}
		if strings.Count(item, "-") == 1 {
			parts := strings.SplitN(item, "-", 2)
			start, startErr := strconv.Atoi(strings.TrimSpace(parts[0]))
			end, endErr := strconv.Atoi(strings.TrimSpace(parts[1]))
			if startErr == nil && endErr == nil && (start > end || end-start > 10000) {
				return fmt.Errorf("条件 %q 的数值范围 %q 无效或过大", field, item)
			}
		}
	}
	return nil
}

func categoryFallbackWarnings(group string, rules []MediaRecognitionCategoryRule) []string {
	warnings := make([]string, 0)
	for index, rule := range rules {
		if rule.Fallback && index < len(rules)-1 {
			warnings = append(warnings, fmt.Sprintf("%s 的兜底分类 %q 后还有规则，后续规则不会被匹配", group, rule.Name))
		}
	}
	return warnings
}

func normalizeMediaRecognitionCategoryYAML(source string) string {
	source = strings.TrimPrefix(source, "\ufeff")
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	return source + "\n"
}

// LoadCategoryConfig 读取已保存分类配置；未保存时返回 MoviePilot 兼容默认值。
func (s *MediaRecognitionService) LoadCategoryConfig() (MediaRecognitionCategoryConfigResult, error) {
	if s == nil || s.db == nil {
		return MediaRecognitionCategoryConfigResult{}, errors.New("数据库未初始化")
	}
	source := DefaultMediaRecognitionCategoryYAML
	configured := false
	var row model.SystemConfig
	err := s.db.Where("config_key = ?", model.ConfigKeyMediaRecognitionCategories).First(&row).Error
	switch {
	case err == nil:
		source = row.ConfigValue
		configured = true
	case errors.Is(err, gorm.ErrRecordNotFound):
	case err != nil:
		return MediaRecognitionCategoryConfigResult{}, err
	}
	config, warnings, err := ParseMediaRecognitionCategoryYAML(source)
	if err != nil {
		if configured {
			return MediaRecognitionCategoryConfigResult{}, fmt.Errorf("数据库中的本地分类配置无效: %w", err)
		}
		return MediaRecognitionCategoryConfigResult{}, err
	}
	return buildMediaRecognitionCategoryConfigResult(configured, source, config, warnings), nil
}

// ValidateCategoryConfig 校验分类 YAML，但不写入数据库。
func (s *MediaRecognitionService) ValidateCategoryConfig(source string) (MediaRecognitionCategoryConfigResult, error) {
	config, warnings, err := ParseMediaRecognitionCategoryYAML(source)
	if err != nil {
		return MediaRecognitionCategoryConfigResult{}, err
	}
	return buildMediaRecognitionCategoryConfigResult(false, source, config, warnings), nil
}

// SaveCategoryConfig 保存完整 category.yaml，后续本地识别和 MP2 降级链路立即使用。
func (s *MediaRecognitionService) SaveCategoryConfig(source string) (MediaRecognitionCategoryConfigResult, error) {
	if s == nil || s.db == nil {
		return MediaRecognitionCategoryConfigResult{}, errors.New("数据库未初始化")
	}
	result, err := s.ValidateCategoryConfig(source)
	if err != nil {
		return MediaRecognitionCategoryConfigResult{}, err
	}
	result.Configured = true

	var existing model.SystemConfig
	err = s.db.Unscoped().Where("config_key = ?", model.ConfigKeyMediaRecognitionCategories).First(&existing).Error
	switch {
	case err == nil:
		err = s.db.Unscoped().Model(&existing).Updates(map[string]any{
			"config_value": result.YAML,
			"config_type":  model.TypeString,
			"category":     model.CategoryMediaRecognition,
			"description":  "FilmFusion 本地媒体分类配置",
			"is_system":    true,
			"is_visible":   true,
			"sort_order":   20,
			"deleted_at":   nil,
		}).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		err = s.db.Create(&model.SystemConfig{
			ConfigKey: model.ConfigKeyMediaRecognitionCategories, ConfigValue: result.YAML,
			ConfigType: model.TypeString, Category: model.CategoryMediaRecognition,
			Description: "FilmFusion 本地媒体分类配置", IsSystem: true,
			IsVisible: true, SortOrder: 20,
		}).Error
	}
	if err != nil {
		return MediaRecognitionCategoryConfigResult{}, err
	}
	return result, nil
}

func buildMediaRecognitionCategoryConfigResult(configured bool, source string, config MediaRecognitionCategoryConfig, warnings []string) MediaRecognitionCategoryConfigResult {
	return MediaRecognitionCategoryConfigResult{
		Configured: configured,
		YAML:       normalizeMediaRecognitionCategoryYAML(source),
		Movie:      config.Movie,
		TV:         config.TV,
		Warnings:   warnings,
	}
}

// MoviePilotCategoryConfig 转换为现有整理链路使用的兼容结构，同时保留 YAML 顺序。
func (config MediaRecognitionCategoryConfig) MoviePilotCategoryConfig() MoviePilotCategoryConfig {
	result := MoviePilotCategoryConfig{
		Movie: make(map[string]*MoviePilotCategoryRule, len(config.Movie)),
		TV:    make(map[string]*MoviePilotCategoryRule, len(config.TV)),
	}
	result.MovieOrder = append(result.MovieOrder, appendMediaRecognitionCategoryRules(result.Movie, config.Movie)...)
	result.TVOrder = append(result.TVOrder, appendMediaRecognitionCategoryRules(result.TV, config.TV)...)
	return result
}

func appendMediaRecognitionCategoryRules(target map[string]*MoviePilotCategoryRule, rules []MediaRecognitionCategoryRule) []string {
	order := make([]string, 0, len(rules))
	for _, item := range rules {
		order = append(order, item.Name)
		if item.Fallback {
			target[item.Name] = nil
			continue
		}
		rule := &MoviePilotCategoryRule{Extra: make(map[string]string)}
		for _, condition := range item.Conditions {
			switch condition.Field {
			case "genre_ids":
				rule.GenreIDs = condition.Value
			case "original_language":
				rule.OriginalLanguage = condition.Value
			case "origin_country":
				rule.OriginCountry = condition.Value
			case "production_countries":
				rule.ProductionCountries = condition.Value
			case "release_year":
				rule.ReleaseYear = condition.Value
			default:
				rule.Extra[condition.Field] = condition.Value
			}
		}
		if len(rule.Extra) == 0 {
			rule.Extra = nil
		}
		target[item.Name] = rule
	}
	return order
}

func mediaRecognitionCategoryConfigFromYAML(source string) (MoviePilotCategoryConfig, error) {
	config, _, err := ParseMediaRecognitionCategoryYAML(source)
	if err != nil {
		return MoviePilotCategoryConfig{}, err
	}
	return config.MoviePilotCategoryConfig(), nil
}

func (s *MediaRecognitionService) moviePilotCategoryConfig(source string, useProvided bool) (MoviePilotCategoryConfig, error) {
	if useProvided {
		return mediaRecognitionCategoryConfigFromYAML(source)
	}
	result, err := s.LoadCategoryConfig()
	if err != nil {
		return MoviePilotCategoryConfig{}, err
	}
	config, _, err := ParseMediaRecognitionCategoryYAML(result.YAML)
	if err != nil {
		return MoviePilotCategoryConfig{}, err
	}
	return config.MoviePilotCategoryConfig(), nil
}

// LoadMoviePilotCategoryConfig 将当前本地 YAML 转为整理链路结构。
func (s *MediaRecognitionService) LoadMoviePilotCategoryConfig() (MoviePilotCategoryConfig, bool, error) {
	result, err := s.LoadCategoryConfig()
	if err != nil {
		return MoviePilotCategoryConfig{}, false, err
	}
	config, err := mediaRecognitionCategoryConfigFromYAML(result.YAML)
	if err != nil {
		return MoviePilotCategoryConfig{}, result.Configured, err
	}
	return config, result.Configured, nil
}

// ApplyConfiguredCategory 只在用户已经保存本地 category.yaml 时覆盖上游分类。
func (s *MediaRecognitionService) ApplyConfiguredCategory(info MoviePilotMediaInfo) (string, bool, error) {
	config, configured, err := s.LoadMoviePilotCategoryConfig()
	if err != nil || !configured {
		return "", configured, err
	}
	return SelectMoviePilotCategory(info.MediaType, info, config), true, nil
}

// classifyMediaRecognitionMediaInfo 应用 category.yaml，并返回命中的分类名。
func classifyMediaRecognitionMediaInfo(media MediaRecognitionMediaInfo, config MoviePilotCategoryConfig) string {
	info := MoviePilotMediaInfo{
		MediaType: media.MediaType, Title: media.Title, OriginalTitle: media.OriginalTitle,
		Year: media.Year, Rating: media.Rating, GenreIDs: media.GenreIDs,
		OriginalLanguages: []string{media.OriginalLanguage}, OriginCountries: media.OriginCountries,
		ProductionCountries: media.ProductionCountries,
		CategoryFields: map[string][]string{
			"media_type":     compactMediaRecognitionValues(media.MediaType),
			"title":          compactMediaRecognitionValues(media.Title),
			"name":           compactMediaRecognitionValues(media.Title),
			"original_title": compactMediaRecognitionValues(media.OriginalTitle),
			"original_name":  compactMediaRecognitionValues(media.OriginalTitle),
			"vote_average":   compactMediaRecognitionValues(strconv.FormatFloat(media.Rating, 'f', -1, 64)),
		},
	}
	if media.OriginalLanguage == "" {
		info.OriginalLanguages = nil
	}
	return SelectMoviePilotCategory(media.MediaType, info, config)
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
