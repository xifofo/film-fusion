package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"
	"film-fusion/app/utils/pathhelper"
)

type AutomationMediaStatusRequest struct {
	UserID           uint
	CloudDirectoryID uint
	TmdbID           string
	Title            string
	Year             string
	MediaType        string
	Category         string
}

type AutomationMediaStatus struct {
	TmdbID          string   `json:"tmdb_id"`
	Title           string   `json:"title"`
	Year            string   `json:"year,omitempty"`
	MediaType       string   `json:"media_type,omitempty"`
	Category        string   `json:"category,omitempty"`
	TargetDir       string   `json:"target_dir,omitempty"`
	LocalDir        string   `json:"local_dir,omitempty"`
	LocalExists     bool     `json:"local_exists"`
	ExistingSeasons []string `json:"existing_seasons,omitempty"`
	EmbyItemID      string   `json:"emby_item_id,omitempty"`
	EmbyURL         string   `json:"emby_url,omitempty"`
	ScanFallback    bool     `json:"scan_fallback,omitempty"`
}

type AutomationMediaStatusChecker interface {
	CheckAutomationMediaStatus(context.Context, AutomationMediaStatusRequest) (AutomationMediaStatus, error)
}

type AutomationHDHiveResource struct {
	Slug             string   `json:"slug"`
	Title            string   `json:"title,omitempty"`
	PanType          string   `json:"pan_type,omitempty"`
	MediaURL         string   `json:"media_url,omitempty"`
	ShareSize        string   `json:"share_size,omitempty"`
	VideoResolution  []string `json:"video_resolution,omitempty"`
	Source           []string `json:"source,omitempty"`
	SubtitleLanguage []string `json:"subtitle_language,omitempty"`
	UnlockPoints     int      `json:"unlock_points,omitempty"`
	IsUnlocked       bool     `json:"is_unlocked"`
}

type AutomationHDHiveUnlockResult struct {
	URL          string `json:"url"`
	AccessCode   string `json:"access_code,omitempty"`
	FullURL      string `json:"full_url,omitempty"`
	AlreadyOwned bool   `json:"already_owned"`
}

type AutomationHDHiveGateway interface {
	QueryAutomationHDHive(context.Context, string, string) ([]AutomationHDHiveResource, error)
	UnlockAutomationHDHive(context.Context, string) (AutomationHDHiveUnlockResult, error)
}

type AutomationEmbyClient interface {
	RefreshLibrary() (int, string, error)
	FindItemByTmdbID(string, string) (*embyhelper.EmbyLookupItem, error)
	WebItemURL(string) string
}

type automationStrmArtifact struct {
	Path    string
	Content string
}

func (s *AutomationService) SetMediaStatusChecker(checker AutomationMediaStatusChecker) {
	if s != nil {
		s.mediaStatus = checker
	}
}

func (s *AutomationService) SetHDHiveGateway(gateway AutomationHDHiveGateway) {
	if s != nil {
		s.hdhive = gateway
	}
}

func (s *AutomationService) SetEmbyClient(client AutomationEmbyClient) {
	if s != nil {
		s.emby = client
	}
}

func (s *AutomationService) executeAutomationMediaExists(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.mediaStatus == nil {
		return nil, errors.New("媒体查重服务未初始化")
	}
	directoryID := automationConfigUint(node.Config, "cloud_directory_id")
	var directory model.CloudDirectory
	if err := s.db.First(&directory, directoryID).Error; err != nil {
		return nil, fmt.Errorf("目录配置不存在: %w", err)
	}
	resolve := func(key string) (string, error) {
		configured := automationConfigString(node.Config, key)
		if configured == "" {
			return "", nil
		}
		return resolveAutomationString(runContext, configured)
	}
	tmdbID, err := resolve("tmdb_id")
	if err != nil {
		return nil, err
	}
	if !automationTMDBIDPattern.MatchString(strings.TrimSpace(tmdbID)) {
		return nil, errors.New("媒体查重需要有效的 TMDB ID")
	}
	title, err := resolve("title")
	if err != nil {
		return nil, err
	}
	year, err := resolve("year")
	if err != nil {
		return nil, err
	}
	mediaType, err := resolve("media_type")
	if err != nil {
		return nil, err
	}
	category, err := resolve("category")
	if err != nil {
		return nil, err
	}
	status, err := s.mediaStatus.CheckAutomationMediaStatus(ctx, AutomationMediaStatusRequest{
		UserID: directory.UserID, CloudDirectoryID: directory.ID, TmdbID: strings.TrimSpace(tmdbID),
		Title: strings.TrimSpace(title), Year: strings.TrimSpace(year), MediaType: strings.TrimSpace(mediaType), Category: strings.TrimSpace(category),
	})
	if err != nil {
		return nil, err
	}
	exists := status.LocalExists || strings.TrimSpace(status.EmbyItemID) != ""
	output := map[string]any{
		"exists": exists, "local_exists": status.LocalExists, "local_dir": status.LocalDir,
		"target_dir": status.TargetDir, "existing_seasons": status.ExistingSeasons,
		"emby_item_id": status.EmbyItemID, "emby_url": status.EmbyURL,
		"tmdb_id": status.TmdbID, "title": status.Title, "year": status.Year,
		"media_type": status.MediaType, "category": status.Category,
		"scan_fallback": status.ScanFallback,
	}
	if exists {
		output["selected_port"] = "exists"
	} else {
		output["selected_port"] = "missing"
	}
	return output, nil
}

func (s *AutomationService) executeAutomationHDHiveQuery(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.hdhive == nil {
		return nil, errors.New("HDHive 自动化服务未初始化")
	}
	resolve := func(key string, required bool) (string, error) {
		configured := automationConfigString(node.Config, key)
		if configured == "" && !required {
			return "", nil
		}
		return resolveAutomationString(runContext, configured)
	}
	tmdbID, err := resolve("tmdb_id", true)
	if err != nil {
		return nil, err
	}
	if !automationTMDBIDPattern.MatchString(strings.TrimSpace(tmdbID)) {
		return nil, errors.New("HDHive 查询需要有效的 TMDB ID")
	}
	mediaType, err := resolve("media_type", true)
	if err != nil {
		return nil, err
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType != "movie" && mediaType != "tv" {
		return nil, errors.New("HDHive 媒体类型必须是 movie 或 tv")
	}
	resolution, err := resolve("resolution", false)
	if err != nil {
		return nil, err
	}
	panType, err := resolve("pan_type", false)
	if err != nil {
		return nil, err
	}
	resources, err := s.hdhive.QueryAutomationHDHive(ctx, mediaType, strings.TrimSpace(tmdbID))
	if err != nil {
		return nil, err
	}
	filtered := make([]AutomationHDHiveResource, 0, len(resources))
	for _, resource := range resources {
		if strings.TrimSpace(resolution) != "" && !automationStringsContain(resource.VideoResolution, resolution) {
			continue
		}
		if strings.TrimSpace(panType) != "" && !strings.EqualFold(strings.TrimSpace(resource.PanType), strings.TrimSpace(panType)) {
			continue
		}
		filtered = append(filtered, resource)
	}
	// Prefer already-owned resources while retaining the provider's ranking.
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].IsUnlocked && !filtered[j].IsUnlocked })
	output := map[string]any{
		"tmdb_id": strings.TrimSpace(tmdbID), "media_type": strings.TrimSpace(mediaType),
		"resource_count": len(filtered), "resources": filtered,
	}
	if len(filtered) == 0 {
		output["selected_port"] = "not_found"
		return output, nil
	}
	selected := filtered[0]
	output["selected_slug"] = selected.Slug
	output["selected_title"] = selected.Title
	output["selected_size"] = selected.ShareSize
	output["selected_resolution"] = selected.VideoResolution
	output["selected_source"] = selected.Source
	output["selected_media_url"] = selected.MediaURL
	output["unlock_points"] = selected.UnlockPoints
	output["is_unlocked"] = selected.IsUnlocked
	output["selected_port"] = "found"
	return output, nil
}

func (s *AutomationService) executeAutomationHDHiveUnlock(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.hdhive == nil {
		return nil, errors.New("HDHive 自动化服务未初始化")
	}
	slug, err := resolveAutomationString(runContext, automationConfigString(node.Config, "slug"))
	if err != nil {
		return nil, err
	}
	result, err := s.hdhive.UnlockAutomationHDHive(ctx, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	downloadURL := strings.TrimSpace(result.FullURL)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(result.URL)
	}
	return map[string]any{
		"slug": strings.TrimSpace(slug), "url": result.URL, "access_code": result.AccessCode,
		"full_url": result.FullURL, "download_url": downloadURL, "already_owned": result.AlreadyOwned,
		"selected_port": "success",
	}, nil
}

func (s *AutomationService) executeAutomationStrmVerify(ctx context.Context, node AutomationNode, definition AutomationDefinition, runContext map[string]any) (map[string]any, error) {
	_, sourceOutput, err := automationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	directoryID := automationConfigUint(node.Config, "cloud_directory_id")
	var directory model.CloudDirectory
	if err := s.db.First(&directory, directoryID).Error; err != nil {
		return nil, fmt.Errorf("目录配置不存在: %w", err)
	}
	root := strings.TrimSpace(directory.SavePath)
	if root == "" {
		return nil, errors.New("目录配置没有 STRM 保存路径")
	}
	paths := collectAutomationStrmPaths(sourceOutput)
	if len(paths) == 0 {
		return map[string]any{
			"valid": false, "checked_count": 0, "valid_count": 0, "invalid_count": 1,
			"errors": []string{"上游整理节点没有返回 STRM 路径"}, "selected_port": "invalid",
		}, nil
	}
	validCount := 0
	errorsFound := make([]string, 0)
	firstContent := ""
	for _, strmPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, verifyErr := verifyAutomationStrmFile(root, strmPath)
		if verifyErr != nil {
			errorsFound = append(errorsFound, strmPath+": "+verifyErr.Error())
			continue
		}
		validCount++
		if firstContent == "" {
			firstContent = content
		}
	}
	valid := len(errorsFound) == 0
	output := map[string]any{
		"valid": valid, "checked_count": len(paths), "valid_count": validCount,
		"invalid_count": len(errorsFound), "strm_path": paths[0], "strm_content": firstContent,
		"errors": errorsFound,
	}
	if valid {
		output["selected_port"] = "valid"
	} else {
		output["selected_port"] = "invalid"
	}
	return output, nil
}

func (s *AutomationService) executeAutomationStrmRegenerate(ctx context.Context, node AutomationNode, definition AutomationDefinition, runContext map[string]any) (map[string]any, error) {
	verifyNodeID, verifyOutput, err := automationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	if automationConfigBool(verifyOutput, "valid") {
		return nil, errors.New("STRM 校验结果为有效，不需要重生成")
	}
	_, organizeOutput, err := automationPredecessorOutput(verifyNodeID, definition, runContext)
	if err != nil {
		return nil, fmt.Errorf("读取上游整理结果失败: %w", err)
	}
	directoryID := automationConfigUint(node.Config, "cloud_directory_id")
	var directory model.CloudDirectory
	if err := s.db.First(&directory, directoryID).Error; err != nil {
		return nil, fmt.Errorf("目录配置不存在: %w", err)
	}
	root := strings.TrimSpace(directory.SavePath)
	if root == "" {
		return nil, errors.New("目录配置没有 STRM 保存路径")
	}
	artifacts := collectAutomationStrmArtifacts(organizeOutput)
	if len(artifacts) == 0 {
		return map[string]any{
			"regenerated_count": 0, "failed_count": 1,
			"errors":        []string{"上游整理节点没有返回可重生成的 STRM 路径和内容"},
			"selected_port": "failure",
		}, nil
	}

	regenerated := make([]string, 0, len(artifacts))
	errorsFound := make([]string, 0)
	for _, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if writeErr := writeAutomationStrmFile(root, artifact.Path, artifact.Content); writeErr != nil {
			errorsFound = append(errorsFound, artifact.Path+": "+writeErr.Error())
			continue
		}
		regenerated = append(regenerated, artifact.Path)
	}
	output := map[string]any{
		"regenerated_count": len(regenerated), "failed_count": len(errorsFound),
		"strm_paths": regenerated, "errors": errorsFound,
	}
	if len(regenerated) > 0 {
		output["strm_path"] = regenerated[0]
	}
	if len(errorsFound) > 0 || len(regenerated) == 0 {
		output["selected_port"] = "failure"
		return output, nil
	}
	output["selected_port"] = "success"
	return output, nil
}

func (s *AutomationService) executeAutomationEmbyRefreshWait(ctx context.Context, nodeRun model.AutomationNodeRun, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.emby == nil {
		return nil, errors.New("Emby 自动化服务未初始化")
	}
	tmdbID, err := resolveAutomationString(runContext, automationConfigString(node.Config, "tmdb_id"))
	if err != nil {
		return nil, err
	}
	mediaType := ""
	if configured := automationConfigString(node.Config, "media_type"); configured != "" {
		mediaType, err = resolveAutomationString(runContext, configured)
		if err != nil {
			return nil, err
		}
	}
	if !automationTMDBIDPattern.MatchString(strings.TrimSpace(tmdbID)) {
		return nil, errors.New("Emby 等待入库需要有效的 TMDB ID")
	}
	output := automationDecodeNodeOutput(nodeRun.OutputJSON)
	delete(output, "selected_port")
	delete(output, "selected_ports")
	delete(output, "reason")
	waitingSince := automationOutputTime(output, "waiting_since")
	if waitingSince.IsZero() {
		waitingSince = time.Now().UTC()
	}
	output["waiting_since"] = waitingSince.Format(time.RFC3339)
	output["tmdb_id"] = strings.TrimSpace(tmdbID)
	output["media_type"] = strings.TrimSpace(mediaType)
	refreshLibrary := true
	if configured := automationOptionalConfigBool(node.Config, "refresh_library"); configured != nil {
		refreshLibrary = *configured
	}
	if refreshLibrary && !automationConfigBool(output, "refresh_requested") {
		status, _, refreshErr := s.emby.RefreshLibrary()
		if refreshErr != nil {
			return output, refreshErr
		}
		output["refresh_requested"] = true
		output["refresh_status"] = status
		output["refresh_requested_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	item, err := s.emby.FindItemByTmdbID(strings.TrimSpace(tmdbID), strings.TrimSpace(mediaType))
	if err != nil {
		return output, err
	}
	waitingSeconds := int(time.Since(waitingSince).Seconds())
	if waitingSeconds < 0 {
		waitingSeconds = 0
	}
	output["waiting_seconds"] = waitingSeconds
	output["last_checked_at"] = time.Now().UTC().Format(time.RFC3339)
	if item != nil && strings.TrimSpace(item.ID) != "" {
		output["found"] = true
		output["waiting"] = false
		output["emby_item_id"] = item.ID
		output["emby_name"] = item.Name
		output["emby_type"] = item.Type
		output["emby_url"] = s.emby.WebItemURL(item.ID)
		output["selected_port"] = "success"
		return output, nil
	}
	maxWaitMinutes := automationConfigUint(node.Config, "max_wait_minutes")
	if maxWaitMinutes == 0 {
		maxWaitMinutes = 30
	}
	if time.Since(waitingSince) >= time.Duration(maxWaitMinutes)*time.Minute {
		output["found"] = false
		output["waiting"] = false
		output["timed_out"] = true
		output["selected_port"] = "failure"
		output["reason"] = fmt.Sprintf("等待 Emby 入库超过 %d 分钟", maxWaitMinutes)
		return output, nil
	}
	pollSeconds := automationConfigUint(node.Config, "poll_interval_seconds")
	if pollSeconds == 0 {
		pollSeconds = 15
	}
	output["found"] = false
	output["waiting"] = true
	output["selected_ports"] = []string{}
	return output, &automationNodeDeferred{
		delay:   time.Duration(pollSeconds) * time.Second,
		message: "等待 Emby 扫描并识别 TMDB " + strings.TrimSpace(tmdbID),
	}
}

func automationStringsContain(values []string, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	for _, value := range values {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), expected) {
			return true
		}
	}
	return false
}

func collectAutomationStrmPaths(output map[string]any) []string {
	paths := automationAnyStringSlice(output["strm_paths"])
	paths = appendUniqueAutomationString(paths, automationAnyString(output["strm_path"]))
	for _, raw := range automationAnySlice(output["items"]) {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		paths = appendUniqueAutomationString(paths, automationAnyString(item["strm_path"]))
	}
	return paths
}

func collectAutomationStrmArtifacts(output map[string]any) []automationStrmArtifact {
	artifacts := make([]automationStrmArtifact, 0)
	seen := map[string]struct{}{}
	add := func(path, content string) {
		path = strings.TrimSpace(path)
		content = strings.TrimSpace(content)
		if path == "" || content == "" {
			return
		}
		if _, exists := seen[path]; exists {
			return
		}
		seen[path] = struct{}{}
		artifacts = append(artifacts, automationStrmArtifact{Path: path, Content: content})
	}
	add(automationAnyString(output["strm_path"]), automationAnyString(output["strm_content"]))
	for _, raw := range automationAnySlice(output["items"]) {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		add(automationAnyString(item["strm_path"]), automationAnyString(item["strm_content"]))
	}
	return artifacts
}

func verifyAutomationStrmFile(root, strmPath string) (string, error) {
	root = strings.TrimSpace(root)
	strmPath = strings.TrimSpace(strmPath)
	if strmPath == "" {
		return "", errors.New("路径为空")
	}
	if !strings.EqualFold(filepath.Ext(strmPath), ".strm") {
		return "", errors.New("文件扩展名不是 .strm")
	}
	pathAbs, err := safeAutomationStrmPath(root, strmPath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(pathAbs)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("不允许校验符号链接")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("不是普通文件")
	}
	if info.Size() > 1024*1024 {
		return "", errors.New("STRM 文件超过 1 MiB")
	}
	contentBytes, err := os.ReadFile(pathAbs)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(contentBytes))
	if err := validateAutomationStrmContent(content); err != nil {
		return "", err
	}
	return content, nil
}

func writeAutomationStrmFile(root, strmPath, content string) error {
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(strmPath)), ".strm") {
		return errors.New("文件扩展名不是 .strm")
	}
	pathAbs, err := safeAutomationStrmPath(root, strmPath)
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(pathAbs); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("不允许覆盖符号链接")
		}
		if !info.Mode().IsRegular() {
			return errors.New("目标不是普通文件")
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	content = strings.TrimSpace(content)
	if err := validateAutomationStrmContent(content); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(pathAbs), ".filmfusion-strm-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(content + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, pathAbs)
}

func safeAutomationStrmPath(root, strmPath string) (string, error) {
	rootAbs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(strings.TrimSpace(strmPath))
	if err != nil {
		return "", err
	}
	if !pathhelper.IsSubPath(pathAbs, rootAbs) {
		return "", errors.New("路径不在目录配置的保存路径内")
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("解析 STRM 根目录失败: %w", err)
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(pathAbs))
	if err != nil {
		return "", fmt.Errorf("解析 STRM 父目录失败: %w", err)
	}
	if !pathhelper.IsSubPath(parentReal, rootReal) {
		return "", errors.New("STRM 父目录通过符号链接逃离了保存路径")
	}
	return pathAbs, nil
}

func validateAutomationStrmContent(content string) error {
	if content == "" {
		return errors.New("STRM 内容为空")
	}
	if strings.ContainsRune(content, '\x00') {
		return errors.New("STRM 内容包含 NUL 字符")
	}
	if parsed, parseErr := url.Parse(content); parseErr == nil && parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("不支持的 STRM 协议 %s", parsed.Scheme)
	}
	return nil
}
