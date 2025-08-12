package embyhelper

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

func IsPlaybackInfoURI(uri string) bool {
	re := regexp.MustCompile(`/[Ii]tems/(\S+)/PlaybackInfo`)
	matches := re.FindStringSubmatch(uri)

	return len(matches) > 0
}

// ReplaceIgnoreCase 不区分大小写替换字符串
func ReplaceIgnoreCase(input string, oldStr string, newStr string) string {
	re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(oldStr))
	return re.ReplaceAllString(input, newStr)
}

func ProxyPlaybackInfo(body []byte, currentURI string) ([]byte, error) {
	var originalData map[string]any

	if err := json.Unmarshal(body, &originalData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	mediaSources, ok := originalData["MediaSources"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid MediaSources format")
	}

	var newMediaSource []map[string]any

	for _, mediaSource := range mediaSources {
		ms, ok := mediaSource.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid media source format")
		}

		itemID, ok := ms["ItemId"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid ItemId format")
		}

		isInfiniteStream, ok := ms["IsInfiniteStream"].(bool)
		if !ok {
			return nil, fmt.Errorf("invalid IsInfiniteStream format")
		}

		container, ok := ms["Container"].(string)
		if !ok {
			container = ""
		}

		ms["SupportsDirectPlay"] = true
		ms["SupportsTranscoding"] = false
		ms["SupportsDirectStream"] = true

		localtionPath := "stream"
		if isInfiniteStream && (container == "" || container == "hls") {
			localtionPath = "master"
			container = "m3u8"
		}

		streamPart := fmt.Sprintf("%s.%s", localtionPath, container)

		replacePath := strings.ReplaceAll(ReplaceIgnoreCase(currentURI, "/items", "/videos"), "PlaybackInfo", streamPart)

		parsedURL, err := url.Parse(replacePath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse URL: %w", err)
		}
		params := parsedURL.Query()
		params.Set("MediaSourceId", ms["Id"].(string))
		params.Set("Static", "true")
		params.Set("ItemId", itemID)
		params.Del("StartTimeTicks")

		parsedURL.RawQuery = params.Encode()
		ms["DirectStreamUrl"] = parsedURL.String()

		newMediaSource = append(newMediaSource, ms)
	}

	originalData["MediaSources"] = newMediaSource

	// 重新编码为 JSON
	modifiedBody, err := json.Marshal(originalData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return modifiedBody, nil
}
