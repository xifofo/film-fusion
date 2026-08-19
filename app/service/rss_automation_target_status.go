package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"film-fusion/app/model"
)

const (
	rssAutomationTargetStatusTimeout = 8 * time.Second
	rssAutomationTargetStatusLimit   = 32 << 20
)

// RSSAutomationTargetStatus is a read-only snapshot of the information shown
// by qBittorrent's status bar. A failed target is returned with Error populated
// so one unavailable downloader does not hide the state of the others.
type RSSAutomationTargetStatus struct {
	TargetID          uint      `json:"target_id"`
	Enabled           bool      `json:"enabled"`
	Online            bool      `json:"online"`
	ConnectionStatus  string    `json:"connection_status,omitempty"`
	DownloadSpeed     int64     `json:"download_speed"`
	UploadSpeed       int64     `json:"upload_speed"`
	DownloadedSession int64     `json:"downloaded_session"`
	UploadedSession   int64     `json:"uploaded_session"`
	ActiveTorrents    *int      `json:"active_torrents,omitempty"`
	DHTNodes          int       `json:"dht_nodes"`
	Error             string    `json:"error,omitempty"`
	CheckedAt         time.Time `json:"checked_at"`
}

type rssAutomationQBTransferInfo struct {
	ConnectionStatus string `json:"connection_status"`
	DownloadSpeed    int64  `json:"dl_info_speed"`
	DownloadedData   int64  `json:"dl_info_data"`
	UploadSpeed      int64  `json:"up_info_speed"`
	UploadedData     int64  `json:"up_info_data"`
	DHTNodes         int    `json:"dht_nodes"`
}

func (s *RSSAutomationService) ListTargetStatuses(ctx context.Context) ([]RSSAutomationTargetStatus, error) {
	targets := make([]model.RSSAutomationTarget, 0)
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&targets).Error; err != nil {
		return nil, err
	}

	statuses := make([]RSSAutomationTargetStatus, len(targets))
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(targets))
	for index := range targets {
		go func(targetIndex int) {
			defer waitGroup.Done()
			statuses[targetIndex] = s.readTargetStatus(ctx, targets[targetIndex])
		}(index)
	}
	waitGroup.Wait()
	return statuses, nil
}

func (s *RSSAutomationService) readTargetStatus(parent context.Context, target model.RSSAutomationTarget) RSSAutomationTargetStatus {
	status := RSSAutomationTargetStatus{
		TargetID:  target.ID,
		Enabled:   target.Enabled,
		CheckedAt: time.Now().UTC(),
	}
	if !target.Enabled {
		return status
	}
	if target.Type != model.RSSAutomationTargetQBittorrent {
		status.Error = fmt.Sprintf("不支持的目标类型 %q", target.Type)
		return status
	}

	ctx, cancel := context.WithTimeout(parent, rssAutomationTargetStatusTimeout)
	defer cancel()
	client, err := s.newRSSAutomationQBClient(ctx, target)
	if err != nil {
		status.Error = err.Error()
		status.CheckedAt = time.Now().UTC()
		return status
	}

	type transferResult struct {
		info rssAutomationQBTransferInfo
		err  error
	}
	type activeResult struct {
		count int
		err   error
	}
	transferChannel := make(chan transferResult, 1)
	activeChannel := make(chan activeResult, 1)
	go func() {
		info, requestErr := client.transferInfo(ctx)
		transferChannel <- transferResult{info: info, err: requestErr}
	}()
	go func() {
		count, requestErr := client.activeTorrentCount(ctx)
		activeChannel <- activeResult{count: count, err: requestErr}
	}()

	transfer := <-transferChannel
	active := <-activeChannel
	status.CheckedAt = time.Now().UTC()
	if transfer.err != nil {
		status.Error = fmt.Sprintf("读取传输状态失败: %v", transfer.err)
		return status
	}

	status.Online = true
	status.ConnectionStatus = transfer.info.ConnectionStatus
	status.DownloadSpeed = transfer.info.DownloadSpeed
	status.UploadSpeed = transfer.info.UploadSpeed
	status.DownloadedSession = transfer.info.DownloadedData
	status.UploadedSession = transfer.info.UploadedData
	status.DHTNodes = transfer.info.DHTNodes
	if active.err != nil {
		status.Error = fmt.Sprintf("读取活动任务失败: %v", active.err)
		return status
	}
	status.ActiveTorrents = &active.count
	return status
}

func (client *rssAutomationQBClient) transferInfo(ctx context.Context) (rssAutomationQBTransferInfo, error) {
	var info rssAutomationQBTransferInfo
	response, err := client.get(ctx, "/api/v2/transfer/info")
	if err != nil {
		return info, err
	}
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, rssAutomationTargetStatusLimit)).Decode(&info); err != nil {
		return info, fmt.Errorf("解析 qBittorrent 传输状态失败: %w", err)
	}
	return info, nil
}

func (client *rssAutomationQBClient) activeTorrentCount(ctx context.Context) (int, error) {
	response, err := client.get(ctx, "/api/v2/torrents/info?filter=active")
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(io.LimitReader(response.Body, rssAutomationTargetStatusLimit))
	token, err := decoder.Token()
	if err != nil {
		return 0, fmt.Errorf("解析 qBittorrent 活动任务失败: %w", err)
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '[' {
		return 0, fmt.Errorf("qBittorrent 活动任务响应不是数组")
	}
	count := 0
	for decoder.More() {
		var item json.RawMessage
		if err := decoder.Decode(&item); err != nil {
			return 0, fmt.Errorf("解析 qBittorrent 活动任务失败: %w", err)
		}
		count++
	}
	if _, err := decoder.Token(); err != nil {
		return 0, fmt.Errorf("解析 qBittorrent 活动任务失败: %w", err)
	}
	return count, nil
}

func (client *rssAutomationQBClient) get(ctx context.Context, path string) (*http.Response, error) {
	request, err := client.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("连接 qBittorrent API 失败: %w", err)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return nil, fmt.Errorf("qBittorrent API 返回 HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}
