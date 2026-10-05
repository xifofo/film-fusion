package organize

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"film-fusion/app/service"
)

func newFilenameRegexProcessor(enabled bool, pattern, replacement string) (filenameRegexProcessor, error) {
	if !enabled {
		return filenameRegexProcessor{}, nil
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return filenameRegexProcessor{}, fmt.Errorf("文件名处理正则不能为空")
	}
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return filenameRegexProcessor{}, fmt.Errorf("文件名处理正则无效: %w", err)
	}
	return filenameRegexProcessor{
		enabled:     true,
		regex:       regex,
		replacement: replacement,
	}, nil
}

func (p filenameRegexProcessor) apply(name string) string {
	if p.enabled && p.regex != nil {
		name = strings.TrimSpace(p.regex.ReplaceAllString(name, p.replacement))
	}
	return normalizeFilenameForRecognition(name)
}

func normalizeFilenameForRecognition(name string) string {
	return strings.ReplaceAll(name, " - ", ".")
}

func (h *Service) recognizeFileWithContext(fileName, recognizeName string, context Organize115FolderContext, recognitionSource string) (service.MoviePilotMediaInfo, string, map[string]any, error) {
	candidates := buildRecognizeInputs(fileName, recognizeName, context)
	var lastErr error
	var lastRaw map[string]any
	lastInput := ""
	for _, input := range candidates {
		lastInput = input
		info, raw, err := h.moviePilotSvc.RecognizeFileWithSource(input, recognitionSource)
		lastRaw = raw
		if err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(info.TmdbID) == "" {
			lastErr = fmt.Errorf("未识别到 TMDB ID")
			continue
		}
		return info, input, raw, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("识别失败")
	}
	return service.MoviePilotMediaInfo{}, lastInput, lastRaw, lastErr
}

// enhanceEpisodeRecognizeInputWithTMDBEnglish handles episode-only release
// names such as "S01E02 - 2160p.WEB-DL...mkv". The normal recognition pass
// first establishes the TMDB identity from folder context. We can then prepend
// TMDB's English title and ask MoviePilot to recognize the enriched filename.
// The enriched input is only accepted when MoviePilot resolves it back to the
// same TMDB entry.
func (h *Service) enhanceEpisodeRecognizeInputWithTMDBEnglish(
	info service.MoviePilotMediaInfo,
	expectedMediaType string,
	recognizeName string,
	currentInput string,
) string {
	return h.enhanceEpisodeRecognizeInputWithTMDBEnglishSource(
		info,
		expectedMediaType,
		recognizeName,
		currentInput,
		"",
	)
}

func (h *Service) enhanceEpisodeRecognizeInputWithTMDBEnglishSource(
	info service.MoviePilotMediaInfo,
	expectedMediaType string,
	recognizeName string,
	currentInput string,
	recognitionSource string,
) string {
	if strings.TrimSpace(recognitionSource) == service.MediaRecognitionSourceLocal {
		return currentInput
	}
	if h == nil || h.tmdbSvc == nil || h.moviePilotSvc == nil {
		return currentInput
	}
	if !episodeLeadingFilenameRegexp.MatchString(path.Base(strings.TrimSpace(recognizeName))) {
		return currentInput
	}

	mediaType := strings.TrimSpace(info.MediaType)
	if mediaType == "" {
		mediaType = strings.TrimSpace(expectedMediaType)
	}
	if !isOrganizeTVMedia(mediaType, info.Category) {
		return currentInput
	}

	tmdbID := strings.TrimSpace(info.TmdbID)
	if tmdbID == "" {
		return currentInput
	}
	englishTitle, err := h.tmdbSvc.GetMediaEnglishTitle(h.requestContext(), tmdbID, mediaType)
	if err != nil {
		if h.logger != nil {
			h.logger.Debugf("[organize] 获取 TMDB 英文标题失败 tmdb_id=%s: %v", tmdbID, err)
		}
		return currentInput
	}

	enhancedInput := buildTMDBEnglishEpisodeRecognizeInput(englishTitle, recognizeName)
	if enhancedInput == "" || enhancedInput == currentInput {
		return currentInput
	}
	var enhancedInfo service.MoviePilotMediaInfo
	if strings.TrimSpace(recognitionSource) == "" {
		enhancedInfo, _, err = h.moviePilotSvc.RecognizeFile(enhancedInput)
	} else {
		enhancedInfo, _, err = h.moviePilotSvc.RecognizeFileWithSource(enhancedInput, recognitionSource)
	}
	if err != nil {
		if h.logger != nil {
			h.logger.Debugf("[organize] 英文标题增强识别失败 source=%s input=%q: %v", recognitionSource, enhancedInput, err)
		}
		return currentInput
	}
	if strings.TrimSpace(enhancedInfo.TmdbID) != tmdbID {
		if h.logger != nil {
			h.logger.Debugf(
				"[organize] 英文标题增强识别 TMDB 不一致 source=%s input=%q got=%s want=%s",
				recognitionSource,
				enhancedInput,
				strings.TrimSpace(enhancedInfo.TmdbID),
				tmdbID,
			)
		}
		return currentInput
	}
	return enhancedInput
}

func buildTMDBEnglishEpisodeRecognizeInput(englishTitle, recognizeName string) string {
	englishTitle = strings.NewReplacer("/", " ", "\\", " ").Replace(strings.TrimSpace(englishTitle))
	englishTitle = strings.Join(strings.Fields(englishTitle), " ")
	englishTitle = strings.Trim(englishTitle, " ._-")
	fileName := strings.TrimLeft(path.Base(strings.TrimSpace(recognizeName)), " ._-")
	if englishTitle == "" || fileName == "" {
		return ""
	}
	return englishTitle + "." + fileName
}

func buildRecognizeInputs(fileName, recognizeName string, context Organize115FolderContext) []string {
	parts := folderContextParts(context)
	parent := ""
	grandparent := ""
	if len(parts) > 0 {
		parent = parts[len(parts)-1]
	}
	if len(parts) > 1 {
		grandparent = parts[len(parts)-2]
	}

	out := make([]string, 0, 12)
	seen := make(map[string]struct{})
	add := func(values ...string) {
		cleaned := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" && (len(cleaned) == 0 || cleaned[len(cleaned)-1] != value) {
				cleaned = append(cleaned, value)
			}
		}
		if len(cleaned) == 0 {
			return
		}
		input := path.Join(cleaned...)
		if input == "." || input == "/" {
			return
		}
		if _, ok := seen[input]; ok {
			return
		}
		seen[input] = struct{}{}
		out = append(out, input)
	}

	names := make([]string, 0, 2)
	nameSeen := make(map[string]struct{})
	addName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := nameSeen[name]; ok {
			return
		}
		nameSeen[name] = struct{}{}
		names = append(names, name)
	}
	addName(recognizeName)
	addName(defaultFallbackRecognizeName(fileName))

	for _, name := range names {
		add(name)
	}
	for _, name := range names {
		add(grandparent, name)
		add(grandparent, parent, name)
		add(parent, name)
		if len(parts) > 0 {
			all := append(append([]string{}, parts...), name)
			add(all...)
		}
	}
	add(fileName)
	return out
}

func defaultFallbackRecognizeName(fileName string) string {
	matches := defaultFilenameFallbackRegexp.FindStringSubmatch(strings.TrimSpace(fileName))
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func folderContextParts(context Organize115FolderContext) []string {
	raw := strings.TrimSpace(context.FolderPath)
	if raw == "" {
		raw = strings.TrimSpace(context.FolderName)
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
