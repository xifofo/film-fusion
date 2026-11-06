// 仅保留旧整理计划反序列化所需的影子结果结构，不再执行 MP2 识别或对比。
package service

// MediaTransferShadowComparison 记录同一输入的 MP2 与本地命名结果。
// 仅用于历史计划，不再由新的识别请求生成。
type MediaTransferShadowComparison struct {
	Status          string         `json:"status"`
	Matched         bool           `json:"matched"`
	MoviePilot      string         `json:"moviepilot,omitempty"`
	Local           string         `json:"local,omitempty"`
	MoviePilotError string         `json:"moviepilot_error,omitempty"`
	LocalError      string         `json:"local_error,omitempty"`
	LocalRaw        map[string]any `json:"local_raw,omitempty"`
}

// MediaRecognitionShadowSnapshot is the stable, engine-neutral subset used to
// decode historical MoviePilot and local comparison data.
// Artwork and descriptive TMDB metadata are deliberately omitted: the shadow
// is measuring recognition identity and release parsing, not provider payload
// completeness.
type MediaRecognitionShadowSnapshot struct {
	Engine        string `json:"engine"`
	MediaType     string `json:"media_type,omitempty"`
	Title         string `json:"title,omitempty"`
	OriginalTitle string `json:"original_title,omitempty"`
	Year          string `json:"year,omitempty"`
	TitleYear     string `json:"title_year,omitempty"`
	TMDBID        string `json:"tmdb_id,omitempty"`
	Category      string `json:"category,omitempty"`
	SeasonEpisode string `json:"season_episode,omitempty"`
	ResourceType  string `json:"resource_type,omitempty"`
	ResourcePix   string `json:"resource_pix,omitempty"`
	VideoEncode   string `json:"video_encode,omitempty"`
	BeginSeason   *int   `json:"begin_season,omitempty"`
}

// MediaRecognitionShadowDifference explains one mismatched field without
// requiring callers to understand either engine's raw response shape.
type MediaRecognitionShadowDifference struct {
	Field      string `json:"field"`
	Label      string `json:"label"`
	MoviePilot string `json:"moviepilot"`
	Local      string `json:"local"`
}

// MediaRecognitionShadowComparison is attached to recognition raw data and is
// retained only for decoding historical organize previews.
type MediaRecognitionShadowComparison struct {
	Medianame       *MedianameRecognition              `json:"medianame,omitempty"`
	Status          string                             `json:"status"`
	Matched         bool                               `json:"matched"`
	MoviePilot      *MediaRecognitionShadowSnapshot    `json:"moviepilot,omitempty"`
	Local           *MediaRecognitionShadowSnapshot    `json:"local,omitempty"`
	Differences     []MediaRecognitionShadowDifference `json:"differences"`
	MoviePilotError string                             `json:"moviepilot_error,omitempty"`
	LocalError      string                             `json:"local_error,omitempty"`
	localInfo       *MoviePilotMediaInfo
	localRaw        map[string]any
}
