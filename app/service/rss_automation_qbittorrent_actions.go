package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"film-fusion/app/model"
)

type rssAutomationMoviePilotTransferer interface {
	ManualTransfer(context.Context, MoviePilotManualTransferRequest) (MoviePilotManualTransferResult, error)
}

type rssAutomationQBTorrentFile struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
}

func (s *RSSAutomationService) executeRSSAutomationMoviePilotTransfer(
	ctx context.Context,
	node RSSAutomationNode,
	definition RSSAutomationDefinition,
	runContext map[string]any,
) (map[string]any, error) {
	if s.mpTransfer == nil {
		return nil, errors.New("MP2 整理服务未初始化")
	}
	_, sourceOutput, err := rssAutomationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	if completed, _ := sourceOutput["completed"].(bool); !completed {
		return nil, errors.New("上游 qBittorrent 下载尚未完成")
	}
	targetID := rssAutomationAnyUint(sourceOutput["target_id"])
	hash := rssAutomationAnyString(sourceOutput["hash"])
	if targetID == 0 || hash == "" {
		return nil, errors.New("上游等待节点没有返回 qBittorrent 下载器或任务 Hash")
	}

	sourcePath := rssAutomationConfigString(node.Config, "source_path")
	if sourcePath != "" {
		sourcePath, err = resolveRSSAutomationString(runContext, sourcePath)
		if err != nil {
			return nil, err
		}
	} else {
		sourcePath = rssAutomationAnyString(sourceOutput["content_path"])
	}
	if strings.TrimSpace(sourcePath) == "" {
		return nil, errors.New("上游等待节点没有返回 qBittorrent 完成路径")
	}

	fileType := strings.ToLower(rssAutomationConfigString(node.Config, "file_type"))
	if fileType == "" || fileType == "auto" {
		fileType = strings.ToLower(rssAutomationAnyString(sourceOutput["content_type"]))
	}
	if fileType != "file" && fileType != "dir" {
		var target model.RSSAutomationTarget
		if err := s.db.First(&target, targetID).Error; err != nil {
			return nil, fmt.Errorf("qBittorrent 下载器不存在: %w", err)
		}
		client, err := s.newRSSAutomationQBClient(ctx, target)
		if err != nil {
			return nil, err
		}
		fileType, _, err = queryRSSAutomationQBContentType(ctx, client, hash)
		if err != nil {
			return nil, fmt.Errorf("无法判断 qBittorrent 下载结果是文件还是目录，请在节点中手动指定: %w", err)
		}
	}

	tmdbID, err := resolveRSSAutomationOptionalTMDBID(runContext, rssAutomationConfigString(node.Config, "tmdb_id"))
	if err != nil {
		return nil, err
	}
	request := MoviePilotManualTransferRequest{
		SourcePath:   sourcePath,
		FileType:     fileType,
		TmdbID:       tmdbID,
		MediaType:    rssAutomationConfigString(node.Config, "media_type"),
		TransferType: rssAutomationConfigString(node.Config, "transfer_type"),
		Scrape:       rssAutomationConfigBool(node.Config, "scrape"),
	}
	result, err := s.mpTransfer.ManualTransfer(ctx, request)
	output := map[string]any{
		"target_id":     targetID,
		"target_name":   rssAutomationAnyString(sourceOutput["target_name"]),
		"torrent_tag":   rssAutomationAnyString(sourceOutput["torrent_tag"]),
		"hash":          hash,
		"name":          rssAutomationAnyString(sourceOutput["name"]),
		"source_path":   sourcePath,
		"content_path":  rssAutomationAnyString(sourceOutput["content_path"]),
		"content_type":  fileType,
		"tmdb_id":       tmdbID,
		"media_type":    request.MediaType,
		"transfer_type": request.TransferType,
		"scrape":        request.Scrape,
		"organized":     err == nil,
		"completed":     err == nil,
	}
	if result.Message != "" {
		output["message"] = result.Message
	}
	if result.Data != nil {
		output["data"] = result.Data
	}
	if err != nil {
		return output, fmt.Errorf("MP2 整理失败: %w", err)
	}
	return output, nil
}

func (s *RSSAutomationService) executeRSSAutomationDeleteQBittorrent(
	ctx context.Context,
	node RSSAutomationNode,
	definition RSSAutomationDefinition,
	runContext map[string]any,
) (map[string]any, error) {
	_, sourceOutput, err := rssAutomationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	targetID := rssAutomationAnyUint(sourceOutput["target_id"])
	hash := rssAutomationAnyString(sourceOutput["hash"])
	if targetID == 0 || hash == "" {
		return nil, errors.New("上游节点没有返回 qBittorrent 下载器或任务 Hash")
	}
	deleteFiles := rssAutomationConfigBool(node.Config, "delete_files")
	if deleteFiles {
		if organized, _ := sourceOutput["organized"].(bool); !organized {
			return nil, errors.New("只有 MP2 整理成功后才能同时删除 qBittorrent 下载文件")
		}
	}

	var target model.RSSAutomationTarget
	if err := s.db.First(&target, targetID).Error; err != nil {
		return nil, fmt.Errorf("qBittorrent 下载器不存在: %w", err)
	}
	if !target.Enabled {
		return nil, errors.New("qBittorrent 下载器已停用")
	}
	client, err := s.newRSSAutomationQBClient(ctx, target)
	if err != nil {
		return nil, err
	}
	torrents, err := queryRSSAutomationQBTorrents(ctx, client, url.Values{"hashes": {hash}})
	if err != nil {
		return nil, err
	}
	output := map[string]any{
		"target_id":    target.ID,
		"target_name":  target.Name,
		"hash":         hash,
		"delete_files": deleteFiles,
	}
	if len(torrents) == 0 {
		output["deleted"] = true
		output["already_missing"] = true
		return output, nil
	}
	torrent := torrents[0]
	if torrent.Progress < 0.999999 && !(torrent.Size > 0 && torrent.AmountLeft == 0) {
		return output, fmt.Errorf("拒绝删除仍未完成的 qBittorrent 任务，当前进度 %.0f%%", torrent.Progress*100)
	}

	form := url.Values{
		"hashes":      {hash},
		"deleteFiles": {fmt.Sprintf("%t", deleteFiles)},
	}
	req, err := client.newRequest(ctx, http.MethodPost, "/api/v2/torrents/delete", strings.NewReader(form.Encode()))
	if err != nil {
		return output, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.client.Do(req)
	if err != nil {
		return output, fmt.Errorf("删除 qBittorrent 任务失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return output, fmt.Errorf("删除 qBittorrent 任务返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	output["name"] = torrent.Name
	output["content_path"] = torrent.ContentPath
	output["deleted"] = true
	output["already_missing"] = false
	output["deleted_at"] = time.Now().UTC().Format(time.RFC3339)
	return output, nil
}

func queryRSSAutomationQBTorrents(ctx context.Context, client *rssAutomationQBClient, values url.Values) ([]rssAutomationQBTorrent, error) {
	requestPath := "/api/v2/torrents/info"
	if len(values) > 0 {
		requestPath += "?" + values.Encode()
	}
	req, err := client.newRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("查询 qBittorrent 任务失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("查询 qBittorrent 任务返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var torrents []rssAutomationQBTorrent
	if err := json.Unmarshal(body, &torrents); err != nil {
		return nil, fmt.Errorf("解析 qBittorrent 任务失败: %w", err)
	}
	return torrents, nil
}

func queryRSSAutomationQBContentType(ctx context.Context, client *rssAutomationQBClient, hash string) (string, int, error) {
	requestPath := "/api/v2/torrents/files?" + url.Values{"hash": {hash}}.Encode()
	req, err := client.newRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := client.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("查询 qBittorrent 文件列表失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("查询 qBittorrent 文件列表返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var files []rssAutomationQBTorrentFile
	if err := json.Unmarshal(body, &files); err != nil {
		return "", 0, fmt.Errorf("解析 qBittorrent 文件列表失败: %w", err)
	}
	if len(files) == 0 {
		return "", 0, errors.New("qBittorrent 任务没有返回文件")
	}
	firstName := strings.Trim(strings.ReplaceAll(files[0].Name, "\\", "/"), "/")
	if len(files) == 1 && !strings.Contains(firstName, "/") {
		return "file", 1, nil
	}
	return "dir", len(files), nil
}
