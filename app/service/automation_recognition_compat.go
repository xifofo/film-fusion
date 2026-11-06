package service

import "encoding/json"

// UnmarshalJSON 将历史 MP2 节点兼容为内置媒体识别，保留节点 ID、连线与参数绑定。
// 旧流程无需用户重新搭建；未知引擎仍由原配置校验拒绝。
func (node *AutomationNode) UnmarshalJSON(data []byte) error {
	type rawNode AutomationNode
	var decoded rawNode
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*node = AutomationNode(decoded)
	switch node.Type {
	case "moviepilot_title_recognize":
		node.Type = AutomationNodeMediaTitleRecognize
	case "moviepilot_recognize":
		node.Type = AutomationNodeMediaFileRecognize
	}
	if source, ok := node.Config["recognition_source"].(string); ok && source != "" {
		if normalized, err := NormalizeMediaRecognitionSource(source); err == nil {
			node.Config["recognition_source"] = normalized
		}
	}
	return nil
}
