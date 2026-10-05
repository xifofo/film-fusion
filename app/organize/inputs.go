package organize

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"film-fusion/app/service"
)

func normalizeOrganizeMediaType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "", nil
	case "movie", "movies", "film", "电影":
		return "movie", nil
	case "tv", "series", "show", "tvshow", "剧集", "电视剧", "番剧", "动漫":
		return "tv", nil
	default:
		return "", fmt.Errorf("媒体类型无效")
	}
}

func sortedCategoryNames(categories map[string]*service.MoviePilotCategoryRule) []string {
	if len(categories) == 0 {
		return nil
	}
	out := make([]string, 0, len(categories))
	for name := range categories {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func mergeCategoryNames(groups ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, group := range groups {
		for _, name := range group {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeFolderIDs(ids []string, fallback string) []string {
	seen := make(map[string]struct{}, len(ids)+1)
	out := make([]string, 0, len(ids)+1)
	add := func(raw string) {
		v := strings.TrimSpace(raw)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, id := range ids {
		add(id)
	}
	add(fallback)
	return out
}

func normalizeFolderContexts(contexts []Organize115FolderContext) map[string]Organize115FolderContext {
	out := make(map[string]Organize115FolderContext, len(contexts))
	for _, context := range contexts {
		folderID := strings.TrimSpace(context.FolderID)
		if folderID == "" {
			continue
		}
		context.FolderID = folderID
		context.FolderName = strings.TrimSpace(context.FolderName)
		context.FolderPath = strings.TrimSpace(context.FolderPath)
		out[folderID] = context
	}
	return out
}

func normalizeFileIDSet(ids []string) map[string]struct{} {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		v := strings.TrimSpace(id)
		if v == "" {
			continue
		}
		seen[v] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	return seen
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func parseExtensions(raw string) []string {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" || strings.EqualFold(cleaned, "null") {
		return nil
	}
	var list []string
	if strings.HasPrefix(cleaned, "[") {
		if err := json.Unmarshal([]byte(cleaned), &list); err == nil {
			return normalizeExtensions(list)
		}
	}
	var single string
	if err := json.Unmarshal([]byte(cleaned), &single); err == nil {
		return normalizeExtensions([]string{single})
	}
	parts := strings.FieldsFunc(cleaned, func(r rune) bool {
		switch r {
		case ',', ';', '|', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
	return normalizeExtensions(parts)
}

func normalizeExtensions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(strings.ToLower(strings.TrimPrefix(v, ".")))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func shouldProcessFileByExtensions(name string, includeExts, excludeExts []string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(name)), "."))
	if ext == "" {
		return len(includeExts) == 0
	}
	if len(includeExts) > 0 && !containsString(includeExts, ext) {
		return false
	}
	if len(excludeExts) > 0 && containsString(excludeExts, ext) {
		return false
	}
	return true
}

func shouldProcessFileBySize(name string, sizeBytes int64, minMB int) bool {
	if minMB <= 0 {
		return true
	}
	if isSubtitleFile(name) {
		return true
	}
	minBytes := int64(minMB) * 1024 * 1024
	return sizeBytes >= minBytes
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
