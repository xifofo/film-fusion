package service

import (
	"film-fusion/app/config"
	"net/url"
	"strconv"
	"strings"
)

const (
	EmbyImageProfileLibraryCover     = "library_cover"
	EmbyImageProfilePoster           = "poster"
	EmbyImageProfileContinueBackdrop = "continue_backdrop"
	EmbyImageProfileListPoster       = "list_poster"
	EmbyImageProfileDetailLogo       = "detail_logo"
	EmbyImageProfileDetailBackdrop   = "detail_backdrop"
	EmbyImageProfileOther            = "other"
)

type EmbyImageOptimizationResult struct {
	IsImage bool
	Profile string
	Query   url.Values
	Changed bool
}

// ApplyEmbyImageOptimization classifies an Emby image URL and clamps its query
// parameters without increasing a client-requested quality or dimension.
func ApplyEmbyImageOptimization(path string, query url.Values, settings config.EmbyImageOptimizationConfig) EmbyImageOptimizationResult {
	cloned := cloneURLValues(query)
	profile, isImage := ClassifyEmbyImageRequest(path, cloned)
	result := EmbyImageOptimizationResult{IsImage: isImage, Profile: profile, Query: cloned}
	if !isImage || !settings.Enabled {
		return result
	}

	rule := embyImageRuleForProfile(settings, profile)
	if !rule.Enabled {
		return result
	}

	result.Changed = clampImageQuery(cloned, "maxWidth", rule.MaxWidth) || result.Changed
	result.Changed = clampImageQuery(cloned, "maxHeight", rule.MaxHeight) || result.Changed
	result.Changed = clampImageQuery(cloned, "quality", rule.Quality) || result.Changed
	return result
}

func ClassifyEmbyImageRequest(path string, query url.Values) (string, bool) {
	cleanPath := strings.TrimPrefix(path, "/emby")
	segments := strings.Split(strings.Trim(cleanPath, "/"), "/")
	imageIndex := -1
	for i, segment := range segments {
		if strings.EqualFold(segment, "Images") {
			imageIndex = i
			break
		}
	}
	if imageIndex < 2 || imageIndex+1 >= len(segments) {
		return "", false
	}
	if !strings.EqualFold(segments[0], "Items") && !strings.EqualFold(segments[0], "Users") {
		return "", false
	}

	imageType := strings.ToLower(segments[imageIndex+1])
	width := queryInt(query, "maxWidth")
	height := queryInt(query, "maxHeight")

	switch imageType {
	case "logo":
		return EmbyImageProfileDetailLogo, true
	case "backdrop":
		hasIndex := imageIndex+2 < len(segments) && strings.TrimSpace(segments[imageIndex+2]) != ""
		if hasIndex || width >= 1000 || height >= 700 {
			return EmbyImageProfileDetailBackdrop, true
		}
		return EmbyImageProfileContinueBackdrop, true
	case "primary":
		if width > 0 && height > 0 && width > height {
			return EmbyImageProfileLibraryCover, true
		}
		if (width > 0 && width <= 200) || (height > 0 && height <= 300) {
			return EmbyImageProfileListPoster, true
		}
		return EmbyImageProfilePoster, true
	case "thumb":
		return EmbyImageProfileOther, true
	default:
		return EmbyImageProfileOther, true
	}
}

func embyImageRuleForProfile(settings config.EmbyImageOptimizationConfig, profile string) config.EmbyImageRuleConfig {
	switch profile {
	case EmbyImageProfileLibraryCover:
		return settings.LibraryCover
	case EmbyImageProfilePoster:
		return settings.Poster
	case EmbyImageProfileContinueBackdrop:
		return settings.ContinueBackdrop
	case EmbyImageProfileListPoster:
		return settings.ListPoster
	case EmbyImageProfileDetailLogo:
		return settings.DetailLogo
	case EmbyImageProfileDetailBackdrop:
		return settings.DetailBackdrop
	default:
		return settings.Other
	}
}

func cloneURLValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func queryInt(values url.Values, name string) int {
	for key, items := range values {
		if !strings.EqualFold(key, name) || len(items) == 0 {
			continue
		}
		value, _ := strconv.Atoi(strings.TrimSpace(items[0]))
		return value
	}
	return 0
}

func clampImageQuery(values url.Values, name string, limit int) bool {
	if limit <= 0 {
		return false
	}
	current := queryInt(values, name)
	if current > 0 && current <= limit {
		return false
	}
	for key := range values {
		if strings.EqualFold(key, name) {
			delete(values, key)
		}
	}
	values.Set(name, strconv.Itoa(limit))
	return true
}
