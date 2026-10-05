package service

import (
	"fmt"
	"strings"
	"time"
)

func (s *MoviePilotService) localRecognitionWords() (string, bool) {
	if s.local == nil {
		return "", false
	}
	words, configured, err := s.local.LoadWords()
	if err != nil {
		if s.logger != nil {
			s.logger.Warnf("[media-recognition] 读取本地识别词失败，继续使用 MoviePilot 配置: %v", err)
		}
		return "", false
	}
	if !configured {
		return "", false
	}
	if len(words) == 0 {
		// MoviePilot 将空 custom_words 当作未传入并回退到自己的全局词表；
		// 用注释占位才能表达 FilmFusion 已显式保存空词表。
		return "# FilmFusion empty word list", true
	}
	return strings.Join(words, "\n"), true
}

func (s *MoviePilotService) searchMediaLocally(keyword string, count int, upstreamErr error) ([]MoviePilotSearchResult, error) {
	if s.local == nil {
		return nil, upstreamErr
	}
	results, localErr := s.local.SearchMedia(s.requestContext(), keyword, count)
	if localErr != nil {
		return nil, fmt.Errorf("MoviePilot 搜索失败: %v；本地搜索也失败: %w", upstreamErr, localErr)
	}
	s.logLocalRecognitionFallback("媒体搜索", upstreamErr)
	return results, nil
}

func (s *MoviePilotService) transferNameLocally(filePath string, upstreamErr error) (string, map[string]any, error) {
	if s.local == nil {
		return "", nil, upstreamErr
	}
	name, raw, localErr := s.local.BuildTransferName(filePath)
	if localErr != nil {
		return "", raw, fmt.Errorf("MoviePilot 重命名失败: %v；本地重命名也失败: %w", upstreamErr, localErr)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	raw["fallback_reason"] = upstreamErr.Error()
	s.logLocalRecognitionFallback("文件命名", upstreamErr)
	return name, raw, nil
}

func (s *MoviePilotService) logLocalRecognitionFallback(action string, err error) {
	s.markLocalRecognitionFallback(err)
	if s.logger != nil {
		s.logger.Warnf("[media-recognition] MoviePilot %s不可用，已切换 FilmFusion 本地能力: %v", action, err)
	}
}

func (s *MoviePilotService) activeLocalRecognitionFallback() (error, bool) {
	state := s.sharedState()
	if s.local == nil {
		return nil, false
	}
	state.mu.RLock()
	until := state.fallbackUntil
	reason := state.fallbackReason
	state.mu.RUnlock()
	if until.IsZero() || !time.Now().Before(until) {
		return nil, false
	}
	return fmt.Errorf("MoviePilot 暂停重试至 %s: %s", until.Format(time.RFC3339), reason), true
}

func (s *MoviePilotService) markLocalRecognitionFallback(err error) {
	state := s.sharedState()
	if s.requestContext().Err() != nil || err == nil || !shouldOpenMoviePilotFallbackCircuit(err) {
		return
	}
	state.mu.Lock()
	state.fallbackUntil = time.Now().Add(moviePilotLocalFallbackTTL)
	state.fallbackReason = err.Error()
	state.mu.Unlock()
}

func (s *MoviePilotService) clearLocalRecognitionFallback() {
	state := s.sharedState()
	state.mu.Lock()
	state.fallbackUntil = time.Time{}
	state.fallbackReason = ""
	state.mu.Unlock()
}

func shouldOpenMoviePilotFallbackCircuit(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "moviepilot 暂停重试至") {
		return false
	}
	for _, businessMessage := range []string{"未识别到媒体", "未返回媒体搜索结果", "未返回重命名结果"} {
		if strings.Contains(message, strings.ToLower(businessMessage)) {
			return false
		}
	}
	return true
}
