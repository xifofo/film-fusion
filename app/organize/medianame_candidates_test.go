package organize

import (
	"encoding/json"
	"strings"
	"testing"

	"film-fusion/app/model"
	"github.com/xifofo/medianame"
	mtmdb "github.com/xifofo/medianame/tmdb"
)

func candidatePreview(t *testing.T, e *planTestEnv) Organize115CookieResult {
	t.Helper()
	preview, err := e.handler.buildOrganize115CookieResult(1, e.req)
	if err != nil || len(preview.Items) == 0 {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	for i := range preview.Groups[0].Items {
		if preview.Groups[0].Items[i].IsSubtitle {
			continue
		}
		item := &preview.Groups[0].Items[i]
		if item.ShadowComparison != nil {
			item = item.ShadowComparison.Medianame
		}
		r := item.Medianame.Result
		other := *r.MediaInfo
		other.TMDBID, other.Title, other.EnglishTitle = "456", "Chosen", "Chosen"
		r.Candidates = append(r.Candidates, mtmdb.Candidate{MediaInfo: &other, Conflicts: []medianame.CandidateConflict{{Field: "tmdb_id", Expected: "123", Actual: "456"}}})
		r.Status, r.MediaInfo = mtmdb.StatusReview, nil
		item.Error, item.TargetPath, item.RenameTo = "medianame: review", "", ""
	}
	preview.Items = preview.Groups[0].Items
	if err := e.db.Model(&model.OrganizePlan{}).Where("id = ?", preview.PlanID).Update("result_json", planJSON(preview)).Error; err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestMedianameCandidateSelectionRebuildsAndExecutesSavedPlan(t *testing.T) {
	e := setupOrganizePlanTest(t)
	preview := candidatePreview(t, e)
	calls := e.calls
	result, err := e.handler.selectMedianameCandidate(1, selectMedianameCandidateRequest{PlanID: preview.PlanID, FileIDs: []string{"101"}, TMDBID: "456", MediaType: "tv"})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("selected: %+v, %v", result, err)
	}
	item := result.Items[0]
	if item.Error != "" || item.TmdbID != "456" || item.SourceSeason != 0 || item.SourceEpisode != 1 || !strings.Contains(item.TargetPath, "Chosen") || !strings.Contains(item.StrmPath, "Chosen") {
		t.Fatalf("candidate did not rebuild identity, episode or paths: %+v", item)
	}
	if result.PlanID == preview.PlanID || result.RecognitionSource != "medianame" || result.Pipeline.Source != "medianame" || !result.DryRun || e.mutations != 0 || calls != e.calls {
		t.Fatalf("expected a new read-only medianame plan using stored details: %+v", result)
	}
	r := item.Medianame.Result
	if r.SelectionBasis != "manual" || r.MetaInfo.IDs.TMDB != "123" || len(r.Candidates[1].ResolvedConflicts) != 1 {
		t.Fatalf("selection lost original evidence: %+v", r)
	}
	_, original, err := e.handler.loadMedianameCandidatePlan(1, preview.PlanID)
	if err != nil || medianamePlanItem(original.Items[0]).Medianame.Result.Status != mtmdb.StatusReview {
		t.Fatal("original plan changed", err)
	}
	e.tmdbFail = true
	apply := e.req
	apply.DryRun, apply.PlanID, apply.RecognitionSource = false, result.PlanID, "medianame"
	applied, err := e.handler.buildOrganize115CookieResult(1, apply)
	if err != nil || applied.Items[0].TmdbID != "456" || e.mutations == 0 || calls != e.calls {
		t.Fatalf("saved selected plan was not replayed: %+v, %v", applied, err)
	}
}

func TestMedianameCandidateSelectionRejectsInvalidAndStaleRequests(t *testing.T) {
	for _, mode := range []string{"foreign_user", "unknown_file", "unknown_id", "wrong_type", "request_error", "executed", "changed_source", "changed_directory", "changed_words"} {
		t.Run(mode, func(t *testing.T) {
			e := setupOrganizePlanTest(t)
			preview := candidatePreview(t, e)
			selection := selectMedianameCandidateRequest{PlanID: preview.PlanID, FileIDs: []string{"101"}, TMDBID: "456", MediaType: "tv"}
			userID := uint(1)
			switch mode {
			case "foreign_user":
				userID = 2
			case "unknown_file":
				selection.FileIDs = []string{"101", "other"}
			case "unknown_id":
				selection.TMDBID = "999"
			case "wrong_type":
				selection.MediaType = "movie"
			case "request_error":
				preview.Groups[0].Items[0].ShadowComparison.Medianame.Medianame.Result.Status = mtmdb.StatusRequestError
				e.db.Model(&model.OrganizePlan{}).Where("id = ?", preview.PlanID).Update("result_json", planJSON(preview))
			case "executed":
				e.db.Model(&model.OrganizePlan{}).Where("id = ?", preview.PlanID).Update("status", "executed")
			case "changed_source":
				e.sourceName = "Changed.S01E02.[tmdb=123].mkv"
			case "changed_words":
				preview.Pipeline.Words = []string{"changed"}
				e.db.Model(&model.OrganizePlan{}).Where("id = ?", preview.PlanID).Update("result_json", planJSON(preview))
			case "changed_directory":
				e.db.Model(&model.CloudDirectory{}).Where("id = 2").Update("directory_name", "Changed")
			}
			if _, err := e.handler.selectMedianameCandidate(userID, selection); err == nil {
				t.Fatal("invalid candidate selection accepted")
			}
			if e.mutations != 0 {
				t.Fatal("selection mutated cloud files")
			}
		})
	}
}

func TestMedianameCandidateSelectionPersistsTaskAndKeepsManualChoiceOnRefresh(t *testing.T) {
	e := setupOrganizePlanTest(t)
	if err := e.db.AutoMigrate(&model.OrganizePreviewTask{}); err != nil {
		t.Fatal(err)
	}
	preview := candidatePreview(t, e)
	task := model.OrganizePreviewTask{UserID: 1, CloudDirectoryID: 2, CloudStorageID: 3, FolderID: "100", Status: model.OrganizePreviewStatusCompleted, ResultJSON: planJSON(preview), RecognitionSource: "shadow"}
	if err := e.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	selection := selectMedianameCandidateRequest{PlanID: preview.PlanID, FileIDs: []string{"101"}, TMDBID: "456", MediaType: "tv", PreviewTaskID: task.ID}
	result, err := e.handler.selectMedianameCandidate(1, selection)
	if err != nil {
		t.Fatal(err)
	}
	e.db.First(&task, task.ID)
	var stored Organize115CookieResult
	if err := json.Unmarshal([]byte(task.ResultJSON), &stored); err != nil || stored.PlanID != result.PlanID || task.RecognitionSource != "medianame" || task.Status != model.OrganizePreviewStatusCompleted {
		t.Fatalf("task selection not persisted: %+v, %v", task, err)
	}
	if _, err := e.handler.selectMedianameCandidate(1, selection); err == nil {
		t.Fatal("stale task selection accepted")
	}
	req := e.req
	req.CandidatePlanID, req.RecognitionSource = result.PlanID, "medianame"
	refreshed, err := e.handler.buildOrganize115CookieResult(1, req)
	if err != nil || refreshed.Items[0].TmdbID != "456" || refreshed.Items[0].Medianame.Result.SelectionBasis != "manual" {
		t.Fatalf("refresh lost manual selection: %+v, %v", refreshed, err)
	}
}

func TestMedianameCandidateBatchKeepsEpisodesAndSubtitleAssociation(t *testing.T) {
	e := setupOrganizePlanTest(t)
	e.additionalFiles = []map[string]any{
		{"fid": "102", "cid": "100", "n": "Example.2024.S00E02.[tmdb=123].mkv", "ico": "mkv", "s": "1000", "pc": "pick-102"},
		{"fid": "103", "cid": "100", "n": "Example.2024.S00E01.[tmdb=123].zh.srt", "ico": "srt", "s": "100", "pc": "pick-103"},
	}
	preview := candidatePreview(t, e)
	result, err := e.handler.selectMedianameCandidate(1, selectMedianameCandidateRequest{PlanID: preview.PlanID, FileIDs: []string{"101", "102"}, TMDBID: "456", MediaType: "tv"})
	if err != nil || result.Total != 2 || len(result.Items) != 3 {
		t.Fatalf("batch preview: %+v, %v", result, err)
	}
	for _, item := range result.Items {
		if item.Error != "" || !strings.Contains(item.TargetPath, "Chosen") {
			t.Fatalf("batch paths: %+v", item)
		}
		switch item.FileID {
		case "101":
			if item.SourceEpisode != 1 || item.TmdbID != "456" {
				t.Fatalf("first episode: %+v", item)
			}
		case "102":
			if item.SourceEpisode != 2 || item.TmdbID != "456" {
				t.Fatalf("second episode: %+v", item)
			}
		case "103":
			if !item.IsSubtitle || item.MatchedVideoFileID != "101" || !strings.Contains(item.RenameTo, "S00E01") {
				t.Fatalf("subtitle: %+v", item)
			}
		}
	}
	// Selecting a different candidate for one file retains the other's manual choice.
	next, err := e.handler.selectMedianameCandidate(1, selectMedianameCandidateRequest{PlanID: result.PlanID, FileIDs: []string{"101"}, TMDBID: "123", MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range next.Items {
		if item.FileID == "101" && item.TmdbID != "123" {
			t.Fatal("new choice not applied")
		}
		if item.FileID == "102" && item.TmdbID != "456" {
			t.Fatal("previous choice lost")
		}
	}
	if e.mutations != 0 {
		t.Fatal("preview mutated sources")
	}
}
