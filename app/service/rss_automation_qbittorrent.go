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

type rssAutomationQBTorrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	Size        int64   `json:"size"`
	Downloaded  int64   `json:"downloaded"`
	AmountLeft  int64   `json:"amount_left"`
	Ratio       float64 `json:"ratio"`
	ETA         int64   `json:"eta"`
}

func (s *RSSAutomationService) executeRSSAutomationWaitQBittorrent(
	ctx context.Context,
	nodeRun model.RSSAutomationNodeRun,
	node RSSAutomationNode,
	definition RSSAutomationDefinition,
	runContext map[string]any,
) (map[string]any, error) {
	_, sourceOutput, err := rssAutomationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	targetID := rssAutomationAnyUint(sourceOutput["target_id"])
	torrentTag := rssAutomationAnyString(sourceOutput["torrent_tag"])
	if targetID == 0 || torrentTag == "" {
		return nil, errors.New("上游 qBittorrent 节点没有返回下载器或任务跟踪标签")
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

	output := rssAutomationDecodeNodeOutput(nodeRun.OutputJSON)
	delete(output, "selected_port")
	delete(output, "selected_ports")
	delete(output, "reason")
	waitingSince := rssAutomationOutputTime(output, "waiting_since")
	if waitingSince.IsZero() {
		waitingSince = time.Now().UTC()
	}
	output["waiting_since"] = waitingSince.Format(time.RFC3339)
	output["target_id"] = target.ID
	output["target_name"] = target.Name
	output["torrent_tag"] = torrentTag
	output["last_checked_at"] = time.Now().UTC().Format(time.RFC3339)

	requestURL := strings.TrimRight(client.baseURL, "/") + "/api/v2/torrents/info?" + url.Values{"tag": {torrentTag}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return output, err
	}
	resp, err := client.client.Do(req)
	if err != nil {
		return output, fmt.Errorf("查询 qBittorrent 任务失败: %w", err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return output, fmt.Errorf("查询 qBittorrent 任务返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var torrents []rssAutomationQBTorrent
	if err := json.Unmarshal(responseBody, &torrents); err != nil {
		return output, fmt.Errorf("解析 qBittorrent 任务失败: %w", err)
	}

	if len(torrents) > 0 {
		torrent := torrents[0]
		output["hash"] = torrent.Hash
		output["name"] = torrent.Name
		output["progress"] = torrent.Progress * 100
		output["state"] = torrent.State
		output["save_path"] = torrent.SavePath
		output["content_path"] = torrent.ContentPath
		output["size"] = torrent.Size
		output["downloaded"] = torrent.Downloaded
		output["amount_left"] = torrent.AmountLeft
		output["ratio"] = torrent.Ratio
		output["eta"] = torrent.ETA
		switch strings.ToLower(strings.TrimSpace(torrent.State)) {
		case "error", "missingfiles", "unknown":
			output["waiting"] = false
			output["completed"] = false
			output["failed"] = true
			output["selected_port"] = "failure"
			output["reason"] = "qBittorrent 任务状态异常: " + torrent.State
			return output, nil
		}
		if torrent.Progress >= 0.999999 || (torrent.Size > 0 && torrent.AmountLeft == 0) {
			output["waiting"] = false
			output["completed"] = true
			output["failed"] = false
			output["progress"] = 100
			output["selected_port"] = "success"
			return output, nil
		}
	}

	maxWaitMinutes := rssAutomationConfigUint(node.Config, "max_wait_minutes")
	if maxWaitMinutes == 0 {
		maxWaitMinutes = 7 * 24 * 60
	}
	if time.Since(waitingSince) >= time.Duration(maxWaitMinutes)*time.Minute {
		output["waiting"] = false
		output["completed"] = false
		output["timed_out"] = true
		output["selected_port"] = "failure"
		output["reason"] = fmt.Sprintf("等待 qBittorrent 下载超过 %d 分钟", maxWaitMinutes)
		return output, nil
	}
	pollSeconds := rssAutomationConfigUint(node.Config, "poll_interval_seconds")
	if pollSeconds == 0 {
		pollSeconds = 30
	}
	output["waiting"] = true
	output["completed"] = false
	output["failed"] = false
	output["selected_ports"] = []string{}
	progress, _ := rssAutomationNumber(output["progress"])
	return output, &rssAutomationNodeDeferred{
		delay:   time.Duration(pollSeconds) * time.Second,
		message: fmt.Sprintf("qBittorrent 下载中，当前约 %.0f%%", progress),
	}
}
