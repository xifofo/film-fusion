package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/utils/embydebug"
)

type balanceDebugCapture struct {
	lines []string
}

func (capture *balanceDebugCapture) Debugf(format string, args ...interface{}) {
	capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
}

// 固定禁用负载均衡，验证诊断可区分绑定就绪、Cookie 缺失和等待转存，且保持原账号兜底结果。
func TestBindingDebugKeepsRoutingWithBalanceDisabled(t *testing.T) {
	for _, scenario := range []string{"ready", "source_cookie_missing", "target_cookie_missing", "transferring"} {
		t.Run(scenario, func(t *testing.T) {
			db := setupBalanceServiceTestDB(t)
			source, target, match := createBalanceCacheBase(t, db, false)
			if scenario == "source_cookie_missing" {
				source.Cookie = ""
			}
			if scenario == "target_cookie_missing" {
				if err := db.Model(&target).Update("cookie", "").Error; err != nil {
					t.Fatal(err)
				}
			}
			match.CloudStorage = &source

			assignment := model.Match302BalanceAssignment{
				Match302ID:        match.ID,
				SourceStorageID:   source.ID,
				PlaybackStorageID: target.ID,
				SourceFilePath:    "/media/source/Test.mkv",
				Forced:            true,
				SourcePickcode:    "private-source-pick",
				TargetPickcode:    "private-target-pick",
				SHA1:              "ABC",
				Size:              123,
				Status:            model.BalanceAssignmentStatusReady,
			}
			if scenario == "transferring" {
				assignment.Status = model.BalanceAssignmentStatusTransferring
				assignment.TargetPickcode = ""
			}
			if err := db.Create(&assignment).Error; err != nil {
				t.Fatal(err)
			}
			file := Web115File{FileID: "file-1", Name: "Test.mkv", PickCode: "private-source-pick", SHA1: "ABC", Size: 123, IsFile: true}
			svc := NewBalanceAssignmentService(nil)
			svc.sourceFile115Svc = &fakeBalanceSourceFileResolver{file: file, found: true, openFile: file, openFound: true}
			capture := &balanceDebugCapture{}
			ctx := embydebug.Start(context.Background(), capture)
			decision, err := svc.ResolvePlayback(ctx, BalancePlaybackRequest{
				Match: &match, SourcePath: assignment.SourceFilePath, MatchedPath: "/library/Test.mkv",
				EmbyItemID: "item-1", ForcedStorageID: target.ID,
			})
			if err != nil {
				t.Fatal(err)
			}

			wantStorage := source.ID
			wantDiagnostic := "reason=source_not_transferable"
			switch scenario {
			case "ready":
				wantStorage = target.ID
				wantDiagnostic = `account_type="binding"`
			case "target_cookie_missing":
				wantDiagnostic = "reason=target_not_transferable"
			case "transferring":
				wantDiagnostic = "reason=transfer_not_ready"
			}
			if decision.PlaybackStorage.ID != wantStorage {
				t.Fatalf("playback route changed: actual=%d want=%d", decision.PlaybackStorage.ID, wantStorage)
			}
			output := strings.Join(capture.lines, "\n")
			if !strings.Contains(output, wantDiagnostic) || !strings.Contains(output, fmt.Sprintf("actual_storage=%d", wantStorage)) {
				t.Fatalf("missing routing diagnostic: %s", output)
			}
			for _, secret := range []string{"private-source-pick", "private-target-pick", "source-cookie", "target-cookie", "source-token"} {
				if strings.Contains(output, secret) {
					t.Errorf("debug output leaked %q", secret)
				}
			}
		})
	}
}
