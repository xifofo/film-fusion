// medianame 预览与路径调用方共用的数据边界，不包含其他模板引擎。
package service

const (
	maxMediaRenameTemplateBytes = 16 << 10
	maxMediaRenameOutputBytes   = 4 << 10
)

type MediaRenamePreviewResult struct {
	Path      string         `json:"path"`
	Template  string         `json:"template"`
	Variables map[string]any `json:"variables"`
	Warnings  []string       `json:"warnings"`
}

type MediaRenameInput struct {
	Info               MoviePilotMediaInfo
	MediaType          string
	Category           string
	RecognizeName      string
	TransferName       string
	OriginalName       string
	LegacyOriginalName string
	RecognitionRaw     map[string]any
	TransferRaw        map[string]any
}
