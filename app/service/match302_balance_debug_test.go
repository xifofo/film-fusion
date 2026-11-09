package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
	for _, scenario := range []string{"ready", "source_cookie_missing", "target_cookie_missing", "transferring", "configured_timeout", "delayed_ready", "transfer_failed", "missing_pickcode"} {
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
			if scenario == "transferring" || scenario == "configured_timeout" || scenario == "delayed_ready" || scenario == "transfer_failed" {
				assignment.Status = model.BalanceAssignmentStatusTransferring
				assignment.TargetPickcode = ""
			}
			if scenario == "missing_pickcode" {
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
			// 模拟秒传在原来 3 秒期限之后完成，以及等待过程中秒传失败。
			if scenario == "delayed_ready" || scenario == "transfer_failed" {
				delay := 4 * time.Second
				updates := map[string]any{"status": model.BalanceAssignmentStatusReady, "target_pickcode": "private-target-pick"}
				if scenario == "transfer_failed" {
					delay = 100 * time.Millisecond
					updates = map[string]any{"status": model.BalanceAssignmentStatusFailed, "last_error": "private-transfer-error"}
				}
				updated := make(chan error, 1)
				time.AfterFunc(delay, func() {
					updated <- db.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignment.ID).Updates(updates).Error
				})
				t.Cleanup(func() {
					if err := <-updated; err != nil {
						t.Error(err)
					}
				})
			}
			started := time.Now()
			waitSeconds := 0 // 验证旧调用方仍使用默认 10 秒。
			if scenario == "configured_timeout" {
				waitSeconds = 2
			}
			if scenario == "delayed_ready" {
				waitSeconds = 6
			}
			decision, err := svc.ResolvePlayback(ctx, BalancePlaybackRequest{
				Match: &match, SourcePath: assignment.SourceFilePath, MatchedPath: "/library/Test.mkv",
				EmbyItemID: "item-1", ForcedStorageID: target.ID,
				ForcedWaitSeconds: waitSeconds,
			})
			if err != nil {
				t.Fatal(err)
			}

			wantStorage := source.ID
			wantDiagnostic := "reason=source_not_transferable"
			wantReason := "源账号凭据缺失或不可用，无法向指定账号秒传"
			switch scenario {
			case "ready", "delayed_ready":
				wantStorage = target.ID
				wantDiagnostic = `account_type="binding"`
				wantReason = ""
			case "target_cookie_missing":
				wantDiagnostic = "reason=target_not_transferable"
				wantReason = "指定账号类型不支持秒传、凭据缺失或不可用"
			case "transferring":
				wantDiagnostic = "reason=transfer_not_ready"
				wantReason = "指定账号秒传等待超时（10秒），目标文件未就绪"
				if elapsed := time.Since(started); elapsed < 10*time.Second {
					t.Fatalf("binding fell back before 10 seconds: %s", elapsed)
				}
			case "transfer_failed":
				wantDiagnostic = "reason=transfer_not_ready"
				wantReason = "指定账号秒传失败"
			case "configured_timeout":
				wantDiagnostic = "wait_timeout_seconds=2"
				wantReason = "指定账号秒传等待超时（2秒），目标文件未就绪"
				if elapsed := time.Since(started); elapsed < 2*time.Second || elapsed >= 6*time.Second {
					t.Fatalf("configured 2-second wait was not applied: %s", elapsed)
				}
			case "missing_pickcode":
				wantDiagnostic = "reason=transfer_not_ready"
				wantReason = "指定账号秒传已就绪但目标 pickcode 缺失"
			}
			if decision.PlaybackStorage.ID != wantStorage {
				t.Fatalf("playback route changed: actual=%d want=%d", decision.PlaybackStorage.ID, wantStorage)
			}
			output := strings.Join(capture.lines, "\n")
			if !strings.Contains(output, wantDiagnostic) || !strings.Contains(output, fmt.Sprintf("actual_storage=%d", wantStorage)) {
				t.Fatalf("missing routing diagnostic: %s", output)
			}
			if wantReason == "" {
				if decision.FallbackReason != "" || decision.Status != "指定账号播放" {
					t.Fatalf("successful binding unexpectedly marked as fallback: %+v", decision)
				}
			} else {
				wantFallback := wantReason + "；后续路由：未启用负载均衡"
				if decision.FallbackReason != wantFallback || decision.Status != "指定账号回退" || !strings.Contains(output, wantReason) {
					t.Fatalf("binding fallback cause lost: status=%q reason=%q logs=%s", decision.Status, decision.FallbackReason, output)
				}
			}
			for _, secret := range []string{"private-source-pick", "private-target-pick", "source-cookie", "target-cookie", "source-token", "private-transfer-error"} {
				if strings.Contains(output, secret) {
					t.Errorf("debug output leaked %q", secret)
				}
			}
		})
	}
}
