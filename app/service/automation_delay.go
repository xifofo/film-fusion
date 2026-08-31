package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"film-fusion/app/model"
)

const automationMaxDelaySeconds int64 = 30 * 24 * 60 * 60

func automationDelayDuration(config map[string]any) (time.Duration, error) {
	seconds, err := automationDelaySeconds(config)
	if err != nil {
		return 0, err
	}
	return time.Duration(seconds) * time.Second, nil
}

func automationDelaySeconds(config map[string]any) (int64, error) {
	raw, exists := config["delay_seconds"]
	if !exists || raw == nil {
		return 0, errors.New("必须配置延迟时长")
	}
	var seconds int64
	switch value := raw.(type) {
	case int:
		seconds = int64(value)
	case int32:
		seconds = int64(value)
	case int64:
		seconds = value
	case uint:
		if uint64(value) > uint64(automationMaxDelaySeconds) {
			return 0, fmt.Errorf("延迟时长不能超过 %d 秒", automationMaxDelaySeconds)
		}
		seconds = int64(value)
	case uint32:
		seconds = int64(value)
	case uint64:
		if value > uint64(automationMaxDelaySeconds) {
			return 0, fmt.Errorf("延迟时长不能超过 %d 秒", automationMaxDelaySeconds)
		}
		seconds = int64(value)
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
			return 0, errors.New("延迟时长必须是整数秒")
		}
		if value > float64(automationMaxDelaySeconds) {
			return 0, fmt.Errorf("延迟时长不能超过 %d 秒", automationMaxDelaySeconds)
		}
		seconds = int64(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, errors.New("延迟时长必须是整数秒")
		}
		seconds = parsed
	default:
		return 0, errors.New("延迟时长必须是整数秒")
	}
	if seconds < 1 {
		return 0, errors.New("延迟时长不能少于 1 秒")
	}
	if seconds > automationMaxDelaySeconds {
		return 0, fmt.Errorf("延迟时长不能超过 %d 秒", automationMaxDelaySeconds)
	}
	return seconds, nil
}

func executeAutomationDelay(nodeRun model.AutomationNodeRun, node AutomationNode, now time.Time) (map[string]any, error) {
	duration, err := automationDelayDuration(node.Config)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	scheduledAt := now
	resumeAt := now.Add(duration)
	if strings.TrimSpace(nodeRun.OutputJSON) != "" {
		var stored struct {
			ScheduledAt string `json:"scheduled_at"`
			ResumeAt    string `json:"resume_at"`
		}
		if err := json.Unmarshal([]byte(nodeRun.OutputJSON), &stored); err != nil {
			return nil, fmt.Errorf("读取延迟节点状态失败: %w", err)
		}
		if stored.ResumeAt == "" {
			return nil, errors.New("延迟节点状态缺少计划继续时间")
		}
		resumeAt, err = time.Parse(time.RFC3339Nano, stored.ResumeAt)
		if err != nil {
			return nil, fmt.Errorf("延迟节点计划继续时间无效: %w", err)
		}
		if stored.ScheduledAt != "" {
			scheduledAt, err = time.Parse(time.RFC3339Nano, stored.ScheduledAt)
			if err != nil {
				return nil, fmt.Errorf("延迟节点开始时间无效: %w", err)
			}
		}
	}
	seconds := int64(duration / time.Second)
	output := map[string]any{
		"delay_seconds": seconds,
		"scheduled_at":  scheduledAt.UTC().Format(time.RFC3339Nano),
		"resume_at":     resumeAt.UTC().Format(time.RFC3339Nano),
	}
	if now.Before(resumeAt) {
		return output, &automationNodeDeferred{
			delay:   resumeAt.Sub(now),
			message: "延迟执行至 " + resumeAt.Local().Format("2006-01-02 15:04:05"),
		}
	}
	output["resumed_at"] = now.Format(time.RFC3339Nano)
	output["selected_port"] = "success"
	return output, nil
}
