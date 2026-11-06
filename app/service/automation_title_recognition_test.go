package service

import (
	"context"
	"strings"
	"testing"
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
	output, err := automation.executeAutomationMediaTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMediaTitleRecognize, Config: map[string]any{
			"input": "$item.title", "tmdb_id": "1396",
		}},
		map[string]any{"item": map[string]any{"title": "示例剧.S01E01.2160p.WEB-DL", "category": "剧集"}},
	)
	if err != nil {
		t.Fatalf("executeAutomationMediaTitleRecognize() error = %v", err)
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
	output, err := automation.executeAutomationMediaTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMediaTitleRecognize, Config: map[string]any{"input": "$item.title"}},
		map[string]any{"item": map[string]any{"title": "Case Closed (1996) [tmdbid=30983]"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recognizer.inputs) == 0 || recognizer.inputs[0] != "Case Closed (1996) [tmdbid=30983]" {
		t.Fatalf("recognition inputs=%#v", recognizer.inputs)
	}
	if output["selected_port"] != "success" || output["requested_tmdb_id"] != "" || output["tmdb_id"] != "30983" {
		t.Fatalf("output=%#v", output)
	}
}

func TestAutomationMoviePilotTitleRecognitionUsesFailurePortForEmptyResponse(t *testing.T) {
	recognizer := &recordingAutomationTitleRecognizer{}
	automation := &AutomationService{moviePilot: recognizer}
	output, err := automation.executeAutomationMediaTitleRecognize(
		context.Background(),
		AutomationNode{Type: AutomationNodeMediaTitleRecognize, Config: map[string]any{"input": "$item.title"}},
		map[string]any{"item": map[string]any{"title": "无法识别的标题"}},
	)
	if err != nil {
		t.Fatalf("empty recognition response should select failure port without a transport error: %v", err)
	}
	if output["selected_port"] != "failure" {
		t.Fatalf("unexpected output: %#v", output)
	}
}
