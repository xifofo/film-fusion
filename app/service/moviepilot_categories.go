package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type MoviePilotCategoryRule struct {
	GenreIDs            string            `json:"genre_ids"`
	OriginalLanguage    string            `json:"original_language"`
	OriginCountry       string            `json:"origin_country"`
	ProductionCountries string            `json:"production_countries"`
	ReleaseYear         string            `json:"release_year"`
	Extra               map[string]string `json:"-"`
}

type MoviePilotCategoryConfig struct {
	Movie      map[string]*MoviePilotCategoryRule `json:"movie"`
	TV         map[string]*MoviePilotCategoryRule `json:"tv"`
	MovieOrder []string                           `json:"-"`
	TVOrder    []string                           `json:"-"`
}

// UnmarshalJSON 保留 MoviePilot 分类对象在 JSON 中的书写顺序。
func (config *MoviePilotCategoryConfig) UnmarshalJSON(data []byte) error {
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(data, &groups); err != nil {
		return err
	}
	var err error
	config.Movie, config.MovieOrder, err = decodeMoviePilotCategoryGroup(groups["movie"])
	if err != nil {
		return fmt.Errorf("解析 movie 分类失败: %w", err)
	}
	config.TV, config.TVOrder, err = decodeMoviePilotCategoryGroup(groups["tv"])
	if err != nil {
		return fmt.Errorf("解析 tv 分类失败: %w", err)
	}
	return nil
}

func decodeMoviePilotCategoryGroup(data json.RawMessage) (map[string]*MoviePilotCategoryRule, []string, error) {
	result := make(map[string]*MoviePilotCategoryRule)
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return result, nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil, errors.New("分类分组必须是对象")
	}
	order := make([]string, 0)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, nil, errors.New("分类名必须是字符串")
		}
		var rule *MoviePilotCategoryRule
		if err := decoder.Decode(&rule); err != nil {
			return nil, nil, err
		}
		result[name] = rule
		order = append(order, name)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	return result, order, nil
}

// UnmarshalJSON 接收 MoviePilot 允许的动态 TMDB 一级字段。
func (rule *MoviePilotCategoryRule) UnmarshalJSON(data []byte) error {
	type knownRule MoviePilotCategoryRule
	var known knownRule
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	*rule = MoviePilotCategoryRule(known)
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	for _, field := range []string{"genre_ids", "original_language", "origin_country", "production_countries", "release_year"} {
		delete(values, field)
	}
	if len(values) == 0 {
		return nil
	}
	rule.Extra = make(map[string]string, len(values))
	for field, raw := range values {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			var scalar any
			if err := json.Unmarshal(raw, &scalar); err != nil {
				return err
			}
			value = fmt.Sprint(scalar)
		}
		rule.Extra[field] = value
	}
	return nil
}

func (s *MoviePilotService) GetCategoryConfig() (MoviePilotCategoryConfig, error) {
	return s.GetCategoryConfigWithSource("")
}

// GetCategoryConfigWithSource 读取所选内置引擎的分类，不请求外部识别服务。
func (s *MoviePilotService) GetCategoryConfigWithSource(source string) (MoviePilotCategoryConfig, error) {
	_, err := s.ResolveMediaRecognitionSource(source)
	if err != nil {
		return MoviePilotCategoryConfig{}, err
	}
	if s.local == nil {
		return MoviePilotCategoryConfig{}, errors.New("medianame 服务未初始化")
	}
	return s.local.MedianameCategoryNames()
}

func SelectMoviePilotCategory(mediaType string, info MoviePilotMediaInfo, cfg MoviePilotCategoryConfig) string {
	var categories map[string]*MoviePilotCategoryRule
	var order []string
	// MP2 may return the Chinese type "电视剧" rather than the local engine's "tv".
	if isTVMediaType(mediaType) {
		categories = cfg.TV
		order = cfg.TVOrder
	} else {
		categories = cfg.Movie
		order = cfg.MovieOrder
	}

	if len(categories) == 0 {
		return ""
	}

	keys := orderedMoviePilotCategoryNames(categories, order)
	for _, name := range keys {
		rule := categories[name]
		if rule == nil {
			return name
		}
		if matchCategoryRule(info, *rule) {
			return name
		}
	}
	return ""
}

func orderedMoviePilotCategoryNames(categories map[string]*MoviePilotCategoryRule, preferred []string) []string {
	result := make([]string, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, name := range preferred {
		if _, exists := categories[name]; !exists {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	rest := make([]string, 0, len(categories)-len(result))
	for name := range categories {
		if _, exists := seen[name]; !exists {
			rest = append(rest, name)
		}
	}
	sortStrings(rest)
	return append(result, rest...)
}

func matchCategoryRule(info MoviePilotMediaInfo, rule MoviePilotCategoryRule) bool {
	if rule.GenreIDs != "" {
		if !matchMoviePilotCategoryCondition(rule.GenreIDs, info.GenreIDs) {
			return false
		}
	}
	if rule.OriginalLanguage != "" {
		if !matchMoviePilotCategoryCondition(rule.OriginalLanguage, info.OriginalLanguages) {
			return false
		}
	}
	if rule.OriginCountry != "" {
		if !matchMoviePilotCategoryCondition(rule.OriginCountry, info.OriginCountries) {
			return false
		}
	}
	if rule.ProductionCountries != "" {
		if !matchMoviePilotCategoryCondition(rule.ProductionCountries, info.ProductionCountries) {
			return false
		}
	}
	if rule.ReleaseYear != "" {
		if !matchMoviePilotCategoryCondition(rule.ReleaseYear, compactMediaRecognitionValues(info.Year)) {
			return false
		}
	}
	for field, value := range rule.Extra {
		dataValues := info.CategoryFields[field]
		if len(dataValues) == 0 {
			dataValues = info.CategoryFields[strings.ToLower(field)]
		}
		if !matchMoviePilotCategoryCondition(value, dataValues) {
			return false
		}
	}
	return true
}

func matchMoviePilotCategoryCondition(rule string, dataValues []string) bool {
	if strings.TrimSpace(rule) == "" {
		return true
	}
	if len(dataValues) == 0 {
		return false
	}
	data := make(map[string]struct{}, len(dataValues))
	for _, value := range dataValues {
		if value = strings.ToUpper(strings.TrimSpace(value)); value != "" {
			data[value] = struct{}{}
		}
	}
	if len(data) == 0 {
		return false
	}

	positive := make([]string, 0)
	negative := make([]string, 0)
	for _, raw := range strings.Split(rule, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		inverted := strings.HasPrefix(raw, "!")
		raw = strings.TrimPrefix(raw, "!")
		values := expandMoviePilotCategoryValue(raw)
		if inverted {
			negative = append(negative, values...)
		} else {
			positive = append(positive, values...)
		}
	}
	if len(positive) > 0 && !categoryValueIntersects(positive, data) {
		return false
	}
	if len(negative) > 0 && categoryValueIntersects(negative, data) {
		return false
	}
	return true
}

func expandMoviePilotCategoryValue(value string) []string {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "-") {
		return compactMediaRecognitionValues(strings.ToUpper(value))
	}
	parts := strings.SplitN(value, "-", 2)
	startText, endText := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	start, startErr := strconv.Atoi(startText)
	end, endErr := strconv.Atoi(endText)
	if startErr != nil || endErr != nil {
		return compactMediaRecognitionValues(strings.ToUpper(startText), strings.ToUpper(endText))
	}
	if start > end || end-start > 10000 {
		return nil
	}
	result := make([]string, 0, end-start+1)
	for current := start; current <= end; current++ {
		result = append(result, strconv.Itoa(current))
	}
	return result
}

func categoryValueIntersects(values []string, data map[string]struct{}) bool {
	for _, value := range values {
		if _, exists := data[strings.ToUpper(strings.TrimSpace(value))]; exists {
			return true
		}
	}
	return false
}

func normalizeList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
