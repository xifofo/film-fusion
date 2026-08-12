package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxRSSAutomationBodyBytes = 8 << 20

type RSSAutomationFieldMapping struct {
	Name           string `json:"name"`
	Selector       string `json:"selector"`
	Type           string `json:"type,omitempty"`
	Required       bool   `json:"required,omitempty"`
	Multiple       bool   `json:"multiple,omitempty"`
	JoinWith       string `json:"join_with,omitempty"`
	MatchAttribute string `json:"match_attribute,omitempty"`
	MatchPattern   string `json:"match_pattern,omitempty"`
}

type RSSAutomationMapping struct {
	ItemSelector string                      `json:"item_selector"`
	Fields       []RSSAutomationFieldMapping `json:"fields"`
}

type RSSAutomationParsedItem struct {
	Fields map[string]any `json:"fields"`
	Errors []string       `json:"errors,omitempty"`
}

type RSSAutomationParsedFeed struct {
	Title     string                    `json:"title"`
	Items     []RSSAutomationParsedItem `json:"items"`
	Selectors []string                  `json:"selectors,omitempty"`
}

type rssAutomationXMLNode struct {
	Name       xml.Name
	Attributes []xml.Attr
	Text       string
	Children   []*rssAutomationXMLNode
}

func DefaultRSSAutomationMapping() RSSAutomationMapping {
	return RSSAutomationMapping{
		ItemSelector: "channel/item",
		Fields: []RSSAutomationFieldMapping{
			{Name: "title", Selector: "title#text", Type: "string", Required: true},
			{Name: "guid", Selector: "guid#text", Type: "string"},
			{Name: "detail_url", Selector: "link#text", Type: "string"},
			{Name: "download_url", Selector: "enclosure@url", Type: "string"},
			{Name: "size_bytes", Selector: "enclosure@length", Type: "integer"},
			{Name: "category", Selector: "category#text", Type: "string", Multiple: true, JoinWith: ", "},
			{Name: "published_at", Selector: "pubDate#text", Type: "datetime"},
		},
	}
}

func DefaultRSSAutomationMappingJSON() string {
	encoded, _ := json.Marshal(DefaultRSSAutomationMapping())
	return string(encoded)
}

func ParseRSSAutomationMapping(raw string) (RSSAutomationMapping, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultRSSAutomationMapping(), nil
	}
	var mapping RSSAutomationMapping
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return mapping, fmt.Errorf("字段映射 JSON 无效: %w", err)
	}
	if err := ValidateRSSAutomationMapping(mapping); err != nil {
		return mapping, err
	}
	return mapping, nil
}

func ValidateRSSAutomationMapping(mapping RSSAutomationMapping) error {
	mapping.ItemSelector = strings.Trim(strings.TrimSpace(mapping.ItemSelector), "/")
	if mapping.ItemSelector == "" {
		return errors.New("条目节点路径不能为空")
	}
	if len(mapping.Fields) == 0 {
		return errors.New("至少需要配置一个字段")
	}
	seen := make(map[string]struct{}, len(mapping.Fields))
	for _, field := range mapping.Fields {
		name := strings.TrimSpace(field.Name)
		if !rssAutomationNodeIDPattern.MatchString(name) {
			return fmt.Errorf("字段名称 %q 无效", field.Name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("字段名称 %q 重复", name)
		}
		seen[name] = struct{}{}
		if strings.TrimSpace(field.Selector) == "" {
			return fmt.Errorf("字段 %s 的节点选择器不能为空", name)
		}
		switch strings.ToLower(strings.TrimSpace(field.Type)) {
		case "", "string", "integer", "number", "boolean", "datetime":
		default:
			return fmt.Errorf("字段 %s 使用了不支持的类型 %q", name, field.Type)
		}
		if field.MatchPattern != "" {
			if _, err := regexp.Compile(field.MatchPattern); err != nil {
				return fmt.Errorf("字段 %s 的属性匹配正则无效: %w", name, err)
			}
		}
	}
	return nil
}

func ParseRSSAutomationFeed(reader io.Reader, mapping RSSAutomationMapping, limit int) (RSSAutomationParsedFeed, error) {
	if err := ValidateRSSAutomationMapping(mapping); err != nil {
		return RSSAutomationParsedFeed{}, err
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxRSSAutomationBodyBytes+1))
	if err != nil {
		return RSSAutomationParsedFeed{}, fmt.Errorf("读取 RSS 内容失败: %w", err)
	}
	if len(body) > maxRSSAutomationBodyBytes {
		return RSSAutomationParsedFeed{}, errors.New("RSS 内容超过 8 MiB 限制")
	}
	root, err := parseRSSAutomationXML(body)
	if err != nil {
		return RSSAutomationParsedFeed{}, err
	}
	itemNodes := findRSSAutomationNodesByPath(root, mapping.ItemSelector)
	if len(itemNodes) == 0 {
		return RSSAutomationParsedFeed{}, fmt.Errorf("没有找到条目节点 %q", mapping.ItemSelector)
	}
	if limit > 0 && len(itemNodes) > limit {
		itemNodes = itemNodes[:limit]
	}
	parsed := RSSAutomationParsedFeed{
		Title: strings.TrimSpace(firstRSSAutomationNodeText(root, "channel/title", "title")),
		Items: make([]RSSAutomationParsedItem, 0, len(itemNodes)),
	}
	if len(itemNodes) > 0 {
		parsed.Selectors = discoverRSSAutomationSelectors(itemNodes[0])
	}
	for _, itemNode := range itemNodes {
		item := RSSAutomationParsedItem{Fields: make(map[string]any, len(mapping.Fields)), Errors: []string{}}
		for _, field := range mapping.Fields {
			values, extractErr := extractRSSAutomationField(itemNode, field)
			if extractErr != nil {
				item.Errors = append(item.Errors, extractErr.Error())
				continue
			}
			if len(values) == 0 {
				if field.Required {
					item.Errors = append(item.Errors, fmt.Sprintf("必填字段 %s 没有匹配到值", field.Name))
				}
				continue
			}
			if field.Multiple {
				joinWith := field.JoinWith
				if joinWith == "" {
					joinWith = ", "
				}
				item.Fields[field.Name] = strings.Join(values, joinWith)
				continue
			}
			converted, convertErr := convertRSSAutomationScalar(values[0], field.Type)
			if convertErr != nil {
				item.Errors = append(item.Errors, fmt.Sprintf("字段 %s: %v", field.Name, convertErr))
				continue
			}
			item.Fields[field.Name] = converted
		}
		parsed.Items = append(parsed.Items, item)
	}
	return parsed, nil
}

func parseRSSAutomationXML(body []byte) (*rssAutomationXMLNode, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	root := &rssAutomationXMLNode{Name: xml.Name{Local: "#document"}}
	stack := []*rssAutomationXMLNode{root}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 RSS XML 失败: %w", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			node := &rssAutomationXMLNode{Name: typed.Name, Attributes: append([]xml.Attr(nil), typed.Attr...)}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 1 {
				stack[len(stack)-1].Text += string(typed)
			}
		}
	}
	if len(root.Children) == 0 {
		return nil, errors.New("RSS XML 为空")
	}
	return root.Children[0], nil
}

func findRSSAutomationNodesByPath(root *rssAutomationXMLNode, path string) []*rssAutomationXMLNode {
	parts := splitRSSAutomationPath(path)
	if len(parts) == 0 {
		return nil
	}
	if strings.EqualFold(parts[0], root.Name.Local) {
		parts = parts[1:]
	}
	current := []*rssAutomationXMLNode{root}
	for _, part := range parts {
		next := make([]*rssAutomationXMLNode, 0)
		for _, node := range current {
			for _, child := range node.Children {
				if strings.EqualFold(child.Name.Local, localRSSAutomationName(part)) {
					next = append(next, child)
				}
			}
		}
		current = next
		if len(current) == 0 {
			break
		}
	}
	return current
}

func extractRSSAutomationField(item *rssAutomationXMLNode, field RSSAutomationFieldMapping) ([]string, error) {
	selector := strings.TrimSpace(field.Selector)
	attribute := ""
	if index := strings.LastIndex(selector, "@"); index > strings.LastIndex(selector, "/") {
		attribute = localRSSAutomationName(selector[index+1:])
		selector = selector[:index]
	} else if strings.HasSuffix(selector, "#text") {
		selector = strings.TrimSuffix(selector, "#text")
	}
	nodes := findRSSAutomationNodesByPath(item, selector)
	values := make([]string, 0, len(nodes))
	var matchPattern *regexp.Regexp
	if strings.TrimSpace(field.MatchPattern) != "" {
		compiled, err := regexp.Compile(field.MatchPattern)
		if err != nil {
			return nil, err
		}
		matchPattern = compiled
	}
	for _, node := range nodes {
		if field.MatchAttribute != "" {
			candidate := rssAutomationXMLAttribute(node, field.MatchAttribute)
			if matchPattern != nil && !matchPattern.MatchString(candidate) {
				continue
			}
			if matchPattern == nil && candidate == "" {
				continue
			}
		}
		value := strings.TrimSpace(node.Text)
		if attribute != "" {
			value = strings.TrimSpace(rssAutomationXMLAttribute(node, attribute))
		}
		if value != "" {
			values = append(values, value)
			if !field.Multiple {
				break
			}
		}
	}
	return values, nil
}

func convertRSSAutomationScalar(value, valueType string) (any, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(valueType)) {
	case "", "string":
		return value, nil
	case "integer":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效整数", value)
		}
		return parsed, nil
	case "number":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效数字", value)
		}
		return parsed, nil
	case "boolean":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效布尔值", value)
		}
		return parsed, nil
	case "datetime":
		parsed := parseRSSAutomationTime(value)
		if parsed == nil {
			return nil, fmt.Errorf("%q 不是支持的日期时间", value)
		}
		return parsed.UTC().Format(time.RFC3339Nano), nil
	default:
		return nil, fmt.Errorf("不支持的类型 %q", valueType)
	}
}

func parseRSSAutomationTime(value string) *time.Time {
	for _, layout := range []string{
		time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
		time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02",
	} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return &parsed
		}
	}
	return nil
}

func discoverRSSAutomationSelectors(item *rssAutomationXMLNode) []string {
	selectors := make([]string, 0, 32)
	var walk func(*rssAutomationXMLNode, string)
	walk = func(node *rssAutomationXMLNode, prefix string) {
		path := node.Name.Local
		if prefix != "" {
			path = prefix + "/" + path
		}
		if strings.TrimSpace(node.Text) != "" {
			selectors = append(selectors, path+"#text")
		}
		for _, attribute := range node.Attributes {
			selectors = append(selectors, path+"@"+attribute.Name.Local)
		}
		for _, child := range node.Children {
			walk(child, path)
		}
	}
	for _, child := range item.Children {
		walk(child, "")
	}
	return selectors
}

func firstRSSAutomationNodeText(root *rssAutomationXMLNode, paths ...string) string {
	for _, path := range paths {
		if nodes := findRSSAutomationNodesByPath(root, path); len(nodes) > 0 {
			if value := strings.TrimSpace(nodes[0].Text); value != "" {
				return value
			}
		}
	}
	return ""
}

func splitRSSAutomationPath(path string) []string {
	raw := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func localRSSAutomationName(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.LastIndex(value, ":"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func rssAutomationXMLAttribute(node *rssAutomationXMLNode, name string) string {
	name = localRSSAutomationName(name)
	for _, attribute := range node.Attributes {
		if strings.EqualFold(attribute.Name.Local, name) {
			return attribute.Value
		}
	}
	return ""
}

func rssAutomationFingerprint(sourceID uint, fields map[string]any) string {
	identity := firstRSSAutomationString(fields, "guid", "download_url", "detail_url")
	if identity == "" {
		identity = firstRSSAutomationString(fields, "title") + "\x00" + firstRSSAutomationString(fields, "published_at")
	}
	sum := sha256.Sum256([]byte(strconv.FormatUint(uint64(sourceID), 10) + "\x00" + identity))
	return hex.EncodeToString(sum[:])
}

func rssAutomationContentKey(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err == nil && strings.EqualFold(parsed.Scheme, "magnet") {
		for _, xt := range parsed.Query()["xt"] {
			const prefix = "urn:btih:"
			if strings.HasPrefix(strings.ToLower(xt), prefix) {
				return "btih:" + strings.ToLower(strings.TrimSpace(xt[len(prefix):]))
			}
		}
	}
	sum := sha256.Sum256([]byte(rawURL))
	return "url:" + hex.EncodeToString(sum[:])
}

func firstRSSAutomationString(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			return text
		}
	}
	return ""
}
