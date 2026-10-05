package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// BuildOrganizeTargetPath uses the saved custom Jinja2-compatible format once
// FilmFusion has taken ownership of rename configuration. Until the first save,
// it deliberately preserves the historical FilmFusion target path.
func (s *MoviePilotService) BuildOrganizeTargetPath(category string, input MediaRenameInput) (string, error) {
	if r, ok := input.RecognitionRaw["medianame"].(MedianameRecognition); ok {
		target, _, err := s.MedianameTarget(s.requestContext(), r, "", category != "", input.Category)
		return target, err
	}

	if s != nil && s.local != nil {
		target, configured, err := s.local.BuildMediaRenameTargetPath(category, input)
		if err != nil {
			return "", err
		}
		if configured {
			return target, nil
		}
	}
	legacyOriginalName := strings.TrimSpace(input.LegacyOriginalName)
	if legacyOriginalName == "" {
		legacyOriginalName = input.OriginalName
	}
	return BuildMoviePilotTargetPath(category, input.Info, input.TransferName, legacyOriginalName), nil
}

type MoviePilotManualTransferRequest struct {
	SourcePath   string
	FileType     string
	TmdbID       string
	MediaType    string
	TransferType string
	Scrape       bool
}

type MoviePilotManualTransferResult struct {
	Message string
	Data    any
}

// ManualTransfer asks MoviePilot to synchronously organize one local file or
// directory. A successful response means the transfer call has returned, so a
// later automation node may safely remove the corresponding downloader task.
func (s *MoviePilotService) ManualTransfer(ctx context.Context, request MoviePilotManualTransferRequest) (MoviePilotManualTransferResult, error) {
	s = s.WithContext(ctx)
	result := MoviePilotManualTransferResult{}
	sourcePath := strings.TrimSpace(request.SourcePath)
	if sourcePath == "" {
		return result, errors.New("MoviePilot 整理源路径不能为空")
	}
	fileType := strings.ToLower(strings.TrimSpace(request.FileType))
	if fileType != "file" && fileType != "dir" {
		return result, errors.New("MoviePilot 整理源类型必须是 file 或 dir")
	}

	normalizedPath := strings.ReplaceAll(sourcePath, "\\", "/")
	name := path.Base(strings.TrimRight(normalizedPath, "/"))
	fileItem := map[string]any{
		"storage": "local",
		"path":    sourcePath,
		"type":    fileType,
		"name":    name,
	}
	if fileType == "file" {
		fileItem["extension"] = strings.TrimPrefix(path.Ext(name), ".")
	}
	payload := map[string]any{
		"fileitem": fileItem,
		"scrape":   request.Scrape,
	}
	if tmdbID := strings.TrimSpace(request.TmdbID); tmdbID != "" {
		parsed, err := strconv.ParseUint(tmdbID, 10, 64)
		if err != nil || parsed == 0 {
			return result, errors.New("MoviePilot 整理使用的 TMDB ID 无效")
		}
		payload["tmdbid"] = parsed
	}
	switch strings.ToLower(strings.TrimSpace(request.MediaType)) {
	case "", "auto":
	case "movie":
		payload["type_name"] = "电影"
	case "tv":
		payload["type_name"] = "电视剧"
	default:
		return result, errors.New("MoviePilot 整理媒体类型必须是 auto/movie/tv")
	}
	if transferType := strings.TrimSpace(request.TransferType); transferType != "" {
		payload["transfer_type"] = transferType
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return result, fmt.Errorf("序列化 MoviePilot 整理请求失败: %w", err)
	}
	requestURL := s.baseURL() + "/api/v1/transfer/manual?background=false"
	perform := func(token string) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(encoded))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		client := *s.client
		client.Timeout = 0
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return body, resp.StatusCode, readErr
	}

	token, err := s.GetAccessToken()
	if err != nil {
		return result, err
	}
	body, statusCode, err := perform(token)
	if err != nil {
		return result, fmt.Errorf("请求 MoviePilot 整理失败: %w", err)
	}
	if statusCode == http.StatusUnauthorized {
		token, err = s.refreshTokenAfterRejection(token)
		if err == nil {
			body, statusCode, err = perform(token)
		}
		if err != nil {
			return result, fmt.Errorf("刷新凭据后请求 MoviePilot 整理失败: %w", err)
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return result, fmt.Errorf("MoviePilot 整理请求失败: HTTP %d %s", statusCode, strings.TrimSpace(string(body)))
	}
	if err := validateMoviePilotSuccess(body); err != nil {
		return result, err
	}
	var response struct {
		Message string `json:"message"`
		Data    any    `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return result, fmt.Errorf("解析 MoviePilot 整理响应失败: %w", err)
	}
	result.Message = strings.TrimSpace(response.Message)
	result.Data = response.Data
	return result, nil
}

func (s *MoviePilotService) TransferName(filePath, fileType string) (string, map[string]any, error) {
	if fallbackErr, active := s.activeLocalRecognitionFallback(); active {
		return s.transferNameLocally(filePath, fallbackErr)
	}
	name, raw, err := s.transferNameWithMoviePilot(filePath, fileType)
	if err == nil {
		s.clearLocalRecognitionFallback()
		return name, raw, nil
	}
	return s.transferNameLocally(filePath, err)
}

// TransferNameWithSource mirrors RecognizeFileWithSource for the naming stage.
func (s *MoviePilotService) TransferNameWithSource(filePath, fileType, source string) (string, map[string]any, error) {
	normalized, err := s.ResolveMediaRecognitionSource(source)
	if err != nil {
		return "", nil, err
	}
	if normalized == MediaRecognitionSourceMedianame {
		r := s.RecognizeMedianame(s.requestContext(), filePath, MediaRecognitionOptions{Mode: MediaRecognitionModeFile, LookupTMDB: true})
		target, vars, err := s.MedianameTarget(s.requestContext(), r, "", false, "")
		if err != nil {
			return "", map[string]any{"medianame": r}, err
		}
		return path.Base(target), map[string]any{"medianame": r, "rename_context": vars, "target_path": target}, nil
	}
	if normalized == MediaRecognitionSourceLocal {
		if s.local == nil {
			return "", nil, errors.New("FilmFusion 本地识别未初始化")
		}
		return s.local.BuildTransferName(filePath)
	}
	name, raw, err := s.transferNameWithMoviePilot(filePath, fileType)
	if err == nil {
		s.clearLocalRecognitionFallback()
	}
	if normalized == MediaRecognitionSourceShadow {
		return s.finishTransferNameWithShadow(filePath, name, raw, err)
	}
	return name, raw, err
}

func (s *MoviePilotService) transferNameWithMoviePilot(filePath, fileType string) (string, map[string]any, error) {
	values := url.Values{}
	values.Set("path", filePath)
	if fileType != "" {
		values.Set("filetype", fileType)
	}

	body, err := s.doGet("/api/v1/transfer/name", values)
	if err != nil {
		return "", nil, err
	}
	if err := validateMoviePilotSuccess(body); err != nil {
		return "", nil, err
	}

	dataMap := unwrapDataMap(body)
	name := extractString(dataMap, "name", "new_name", "file_name", "filename", "title")
	if name == "" {
		if rawName, ok := dataMap["data"]; ok {
			if str, ok := rawName.(string); ok {
				name = str
			}
		}
	}

	if strings.TrimSpace(name) == "" {
		return "", dataMap, errors.New("MoviePilot 未返回重命名结果")
	}
	return name, dataMap, nil
}

func BuildMoviePilotTargetPath(category string, info MoviePilotMediaInfo, transferName, originalName string) string {
	folderName := strings.TrimSpace(info.TitleYear)
	if folderName == "" {
		folderName = strings.TrimSpace(transferName)
	}
	if folderName == "" {
		folderName = strings.TrimSpace(info.Title)
		if folderName == "" {
			folderName = strings.TrimSuffix(originalName, path.Ext(originalName))
		}
		if info.Year != "" && !strings.Contains(folderName, info.Year) {
			folderName = fmt.Sprintf("%s (%s)", folderName, info.Year)
		}
	} else if folderName == strings.TrimSpace(transferName) {
		folderName = strings.TrimSuffix(folderName, path.Ext(folderName))
	}

	if tmdbID := strings.TrimSpace(info.TmdbID); tmdbID != "" && !strings.Contains(folderName, "{tmdb-") {
		folderName = strings.TrimRight(folderName, " ") + " {tmdb-" + tmdbID + "}"
	}

	fileName := strings.TrimSpace(transferName)
	if fileName == "" {
		fileName = originalName
	} else {
		originalExt := path.Ext(strings.TrimSpace(originalName))
		if originalExt != "" && !strings.EqualFold(path.Ext(fileName), originalExt) {
			// MoviePilot 可能返回以 .10bit、.x265 等发布标签结尾的名称。
			// path.Ext 会把这些标签误认为扩展名；整理只重命名、不转码，因此始终保留源文件真实后缀。
			fileName = strings.TrimRight(fileName, " ") + originalExt
		}
	}

	basePath := path.Join("/", folderName)
	if strings.TrimSpace(category) != "" {
		basePath = path.Join("/", category, folderName)
	}
	if shouldAddSeasonFolder(info) {
		basePath = path.Join(basePath, fmt.Sprintf("Season %02d", info.BeginSeason))
	}

	return path.Join(basePath, fileName)
}

func shouldAddSeasonFolder(info MoviePilotMediaInfo) bool {
	if !info.HasBeginSeason {
		return false
	}
	if isTVMediaType(info.MediaType) {
		return true
	}
	if isTVCategory(info.Category) {
		return true
	}
	return false
}

func isTVMediaType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tv", "series", "tvshow", "show", "电视剧":
		return true
	default:
		return false
	}
}

func isTVCategory(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	if strings.Contains(value, "剧集") {
		return true
	}
	return value == "tv" || value == "series"
}
