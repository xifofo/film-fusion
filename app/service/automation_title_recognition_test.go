package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
)

type recordingAutomationTitleRecognizer struct {
	inputs []string
	info   MoviePilotMediaInfo
}

type recordingAutomationNotifier struct {
	event NotificationEvent
}

func (r *recordingAutomationNotifier) Publish(_ context.Context, event NotificationEvent) NotificationReport {
	r.event = event
	return NotificationReport{Event: event.Type, Deliveries: []NotificationDelivery{{Channel: "test", Success: true}}}
}

func (*recordingAutomationNotifier) Ready(NotificationEventType) bool { return true }

func (r *recordingAutomationTitleRecognizer) RecognizeTitle(input string) (MoviePilotMediaInfo, map[string]any, error) {
	r.inputs = append(r.inputs, input)
	return r.info, nil, nil
}

func TestAutomationNotificationRendersPosterURL(t *testing.T) {
	notifier := &recordingAutomationNotifier{}
	automation := &AutomationService{notifier: notifier}
	output, err := automation.executeAutomationNotification(
		context.Background(),
		AutomationNode{Type: AutomationNodeNotification, Config: map[string]any{
			"title": "{{item.title}}", "message": "识别完成", "image_url": "{{nodes.mp.output.poster_url}}",
		}},
		map[string]any{
			"item":  map[string]any{"title": "示例剧"},
			"nodes": map[string]any{"mp": map[string]any{"output": map[string]any{"poster_url": "https://image.example/poster.jpg"}}},
		},
	)
	if err != nil {
		t.Fatalf("executeAutomationNotification() error = %v", err)
	}
	if notifier.event.ImageURL != "https://image.example/poster.jpg" {
		t.Fatalf("notification ImageURL = %q", notifier.event.ImageURL)
	}
	if output["image_url"] != notifier.event.ImageURL {
		t.Fatalf("notification output = %#v", output)
	}
}

func (r *recordingAutomationTitleRecognizer) RecognizeFile(input string) (MoviePilotMediaInfo, map[string]any, error) {
	return r.RecognizeTitle(input)
}

func (r *recordingAutomationTitleRecognizer) SearchMedia(_ string, _ int) ([]MoviePilotSearchResult, error) {
	return nil, nil
}

func TestAutomationMoviePilotTitleRecognitionSupportsTMDBAssist(t *testing.T) {
	recognizer := &recordingAutomationTitleRecognizer{info: MoviePilotMediaInfo{
		Title: "示例剧", Year: "2026", MediaType: "TV", Category: "国产剧",
		SeasonEpisode: "S01E01", Rating: 8.6, TmdbID: "1396", PosterPath: "/poster.jpg",
	}}
	automation := &AutomationService{moviePilot: recognizer}
	output, err := automation.executeAutomationMoviePilotTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMoviePilotTitle, Config: map[string]any{
			"input": "$item.title", "tmdb_id": "1396",
		}},
		map[string]any{"item": map[string]any{"title": "示例剧.S01E01.2160p.WEB-DL", "category": "剧集"}},
	)
	if err != nil {
		t.Fatalf("executeAutomationMoviePilotTitleRecognize() error = %v", err)
	}
	if len(recognizer.inputs) == 0 || !strings.Contains(recognizer.inputs[0], "{tmdb-1396}") {
		t.Fatalf("recognition inputs = %#v, want TMDB marker first", recognizer.inputs)
	}
	if output["selected_port"] != "success" || output["tmdb_id"] != "1396" {
		t.Fatalf("unexpected output: %#v", output)
	}
	if output["poster_url"] != "https://image.tmdb.org/t/p/w780/poster.jpg" {
		t.Fatalf("poster_url = %#v", output["poster_url"])
	}
}

func TestAutomationMoviePilotTitleRecognitionUsesEmbeddedTMDBMarker(t *testing.T) {
	recognizer := &recordingAutomationTitleRecognizer{info: MoviePilotMediaInfo{
		Title: "名侦探柯南", Year: "1996", MediaType: "tv", TmdbID: "30983",
	}}
	automation := &AutomationService{moviePilot: recognizer}
	output, err := automation.executeAutomationMoviePilotTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title"}},
		map[string]any{"item": map[string]any{"title": "Case Closed (1996) [tmdbid=30983]"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recognizer.inputs) == 0 || recognizer.inputs[0] != "Case Closed (1996).{tmdb-30983}" {
		t.Fatalf("recognition inputs=%#v", recognizer.inputs)
	}
	if output["selected_port"] != "success" || output["requested_tmdb_id"] != "30983" || output["tmdb_id"] != "30983" {
		t.Fatalf("output=%#v", output)
	}
}

func TestAutomationMoviePilotTitleRecognitionReclassifiesWithFilmFusionTMDBDetails(t *testing.T) {
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/326556" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":326556,"name":"斩神之凡尘神域Ⅱ","original_name":"斩神之凡尘神域Ⅱ",
			"first_air_date":"2026-06-25","vote_average":8.3,"genres":[{"id":16,"name":"动画"}],
			"original_language":"zh","origin_country":["CN"]
		}`))
	}))
	defer tmdbServer.Close()

	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: tmdbServer.URL, APIKey: "test-key", TimeoutSeconds: 2,
	}}, nil)
	local := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	if _, err := local.SaveCategoryConfig(`
movie:
  其他电影:
tv:
  动画番剧:
    genre_ids: '16'
  国产剧集:
    origin_country: 'CN'
  其他剧集:
`); err != nil {
		t.Fatal(err)
	}

	recognizer := &recordingAutomationTitleRecognizer{info: MoviePilotMediaInfo{
		Title: "斩神之凡尘神域Ⅱ", Year: "2026", MediaType: "tv", Category: "国产剧集",
		SeasonEpisode: "S01E11", TmdbID: "326556",
	}}
	automation := &AutomationService{moviePilot: recognizer, localMedia: local}
	output, err := automation.executeAutomationMoviePilotTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title"}},
		map[string]any{"item": map[string]any{
			"title": "Slay.The.Gods.2026.S01E11.1080p.WeTV.WEB-DL.AAC2.0.H.264-DepWeb.mkv",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "success" || output["tmdb_id"] != "326556" {
		t.Fatalf("unexpected output: %#v", output)
	}
	if output["category"] != "动画番剧" {
		t.Fatalf("category=%q, want FilmFusion category from complete TMDB details", output["category"])
	}
}

func TestAutomationMoviePilotTitleRecognitionKeepsMoviePilotCategoryWhenTMDBDetailsFail(t *testing.T) {
	recognizer := &recordingAutomationTitleRecognizer{info: MoviePilotMediaInfo{
		Title: "示例剧", Year: "2026", MediaType: "tv", Category: "MoviePilot 分类",
		SeasonEpisode: "S01E01", TmdbID: "1396",
	}}
	local := &recordingAutomationLocalRecognizer{fn: func(string, MediaRecognitionOptions) (MediaRecognitionResult, error) {
		return MediaRecognitionResult{}, errors.New("TMDB 暂时不可用")
	}}
	automation := &AutomationService{moviePilot: recognizer, localMedia: local}
	output, err := automation.executeAutomationMoviePilotTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title"}},
		map[string]any{"item": map[string]any{"title": "Example.Show.2026.S01E01.1080p.WEB-DL"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.calls) != 1 || local.calls[0].input != "示例剧.2026.S01E01.{[tmdbid=1396;type=tv]}" {
		t.Fatalf("local category lookup calls = %#v", local.calls)
	}
	if output["selected_port"] != "success" || output["category"] != "MoviePilot 分类" {
		t.Fatalf("unexpected fallback output: %#v", output)
	}
}

func TestAutomationMoviePilotTitleRecognitionUsesFailurePortForEmptyResponse(t *testing.T) {
	recognizer := &recordingAutomationTitleRecognizer{}
	automation := &AutomationService{moviePilot: recognizer}
	output, err := automation.executeAutomationMoviePilotTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title"}},
		map[string]any{"item": map[string]any{"title": "无法识别的标题"}},
	)
	if err != nil {
		t.Fatalf("empty recognition response should select failure port without a transport error: %v", err)
	}
	if output["selected_port"] != "failure" {
		t.Fatalf("unexpected output: %#v", output)
	}
}
