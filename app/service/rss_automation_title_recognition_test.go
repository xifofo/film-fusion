package service

import (
	"context"
	"strings"
	"testing"
)

type recordingRSSAutomationTitleRecognizer struct {
	inputs []string
	info   MoviePilotMediaInfo
}

type recordingRSSAutomationNotifier struct {
	event NotificationEvent
}

func (r *recordingRSSAutomationNotifier) Publish(_ context.Context, event NotificationEvent) NotificationReport {
	r.event = event
	return NotificationReport{Event: event.Type, Deliveries: []NotificationDelivery{{Channel: "test", Success: true}}}
}

func (*recordingRSSAutomationNotifier) Ready(NotificationEventType) bool { return true }

func (r *recordingRSSAutomationTitleRecognizer) RecognizeTitle(input string) (MoviePilotMediaInfo, map[string]any, error) {
	r.inputs = append(r.inputs, input)
	return r.info, nil, nil
}

func TestRSSAutomationNotificationRendersPosterURL(t *testing.T) {
	notifier := &recordingRSSAutomationNotifier{}
	automation := &RSSAutomationService{notifier: notifier}
	output, err := automation.executeRSSAutomationNotification(
		context.Background(),
		RSSAutomationNode{Type: RSSAutomationNodeNotification, Config: map[string]any{
			"title": "{{item.title}}", "message": "识别完成", "image_url": "{{nodes.mp.output.poster_url}}",
		}},
		map[string]any{
			"item":  map[string]any{"title": "示例剧"},
			"nodes": map[string]any{"mp": map[string]any{"output": map[string]any{"poster_url": "https://image.example/poster.jpg"}}},
		},
	)
	if err != nil {
		t.Fatalf("executeRSSAutomationNotification() error = %v", err)
	}
	if notifier.event.ImageURL != "https://image.example/poster.jpg" {
		t.Fatalf("notification ImageURL = %q", notifier.event.ImageURL)
	}
	if output["image_url"] != notifier.event.ImageURL {
		t.Fatalf("notification output = %#v", output)
	}
}

func (r *recordingRSSAutomationTitleRecognizer) RecognizeFile(input string) (MoviePilotMediaInfo, map[string]any, error) {
	return r.RecognizeTitle(input)
}

func (r *recordingRSSAutomationTitleRecognizer) SearchMedia(_ string, _ int) ([]MoviePilotSearchResult, error) {
	return nil, nil
}

func TestRSSAutomationMoviePilotTitleRecognitionSupportsTMDBAssist(t *testing.T) {
	recognizer := &recordingRSSAutomationTitleRecognizer{info: MoviePilotMediaInfo{
		Title: "示例剧", Year: "2026", MediaType: "TV", Category: "国产剧",
		SeasonEpisode: "S01E01", Rating: 8.6, TmdbID: "1396", PosterPath: "/poster.jpg",
	}}
	automation := &RSSAutomationService{moviePilot: recognizer}
	output, err := automation.executeRSSAutomationMoviePilotTitleRecognize(
		context.Background(),
		RSSAutomationNode{Type: RSSAutomationNodeMoviePilotTitle, Config: map[string]any{
			"input": "$item.title", "tmdb_id": "1396",
		}},
		map[string]any{"item": map[string]any{"title": "示例剧.S01E01.2160p.WEB-DL", "category": "剧集"}},
	)
	if err != nil {
		t.Fatalf("executeRSSAutomationMoviePilotTitleRecognize() error = %v", err)
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

func TestRSSAutomationMoviePilotTitleRecognitionUsesFailurePortForEmptyResponse(t *testing.T) {
	recognizer := &recordingRSSAutomationTitleRecognizer{}
	automation := &RSSAutomationService{moviePilot: recognizer}
	output, err := automation.executeRSSAutomationMoviePilotTitleRecognize(
		context.Background(),
		RSSAutomationNode{Type: RSSAutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title"}},
		map[string]any{"item": map[string]any{"title": "无法识别的标题"}},
	)
	if err != nil {
		t.Fatalf("empty recognition response should select failure port without a transport error: %v", err)
	}
	if output["selected_port"] != "failure" {
		t.Fatalf("unexpected output: %#v", output)
	}
}
