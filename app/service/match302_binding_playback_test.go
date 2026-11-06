package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/store/embyproxylog"
)

func TestBoundPlaybackNeverFallsBackWhenStorageUnavailable(t *testing.T) {
	for _, balance := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			change func(*model.CloudStorage)
			want   string
		}{
			{"missing", func(s *model.CloudStorage) { s.ID = 9999 }, "不存在"},
			{"disabled", func(s *model.CloudStorage) { s.Status = model.StatusDisabled }, "已停用"},
			{"no credentials", func(s *model.CloudStorage) { s.Cookie = "" }, "均缺失"},
			{"cookie only", func(s *model.CloudStorage) {
				s.Cookie = ""
				s.AccessToken = "unused"
				s.Match302AccessMode = model.Match302AccessModeCookieOnly
			}, "Cookie 缺失"},
			{"openapi only", func(s *model.CloudStorage) { s.Match302AccessMode = model.Match302AccessModeOpenAPIOnly }, "AccessToken 缺失"},
		} {
			t.Run(tc.name+map[bool]string{false: "/balance-off", true: "/balance-on"}[balance], func(t *testing.T) {
				db := setupBalanceServiceTestDB(t)
				source, target, match := createBalanceCacheBase(t, db, balance)
				match.CloudStorage = &source
				tc.change(&target)
				if target.ID != 9999 {
					if err := db.Save(&target).Error; err != nil {
						t.Fatal(err)
					}
				}
				svc := NewBalanceAssignmentService(nil)
				// 不可用时必须在请求源文件/启动秒传前失败。
				resolver := &fakeBalanceSourceFileResolver{}
				svc.sourceFile115Svc = resolver
				decision, err := svc.ResolvePlayback(context.Background(), BalancePlaybackRequest{Match: &match, ForcedStorageID: target.ID})
				if decision != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("decision=%+v err=%v, want %s", decision, err, tc.want)
				}
				if resolver.openCalls+resolver.cookieCalls != 0 {
					t.Fatal("unavailable binding requested source file")
				}
			})
		}
	}
}

func TestBoundPlaybackUsesOnlyBoundReadyAssignment(t *testing.T) {
	for _, balance := range []bool{false, true} {
		for _, sourceBound := range []bool{false, true} {
			db := setupBalanceServiceTestDB(t)
			source, target, match := createBalanceCacheBase(t, db, balance)
			source.Match302AccessMode = model.Match302AccessModeCookieOnly
			match.CloudStorage = &source
			bound := target.ID
			if sourceBound {
				bound = source.ID
			}
			a := model.Match302BalanceAssignment{Match302ID: match.ID, SourceFilePath: "/media/source/Test.mkv", SourceStorageID: source.ID, PlaybackStorageID: target.ID, Forced: true, Status: model.BalanceAssignmentStatusReady, TargetPickcode: "bound-pick", SourcePickcode: "source-pick", SHA1: "ABC", Size: 123}
			if err := db.Create(&a).Error; err != nil {
				t.Fatal(err)
			}
			svc := NewBalanceAssignmentService(nil)
			svc.sourceFile115Svc = &fakeBalanceSourceFileResolver{file: Web115File{FileID: "file", PickCode: "source-pick", IsFile: true}, found: true}
			d, err := svc.ResolvePlayback(context.Background(), BalancePlaybackRequest{Match: &match, SourcePath: a.SourceFilePath, MatchedPath: "/library/Test.mkv", ForcedStorageID: bound})
			if err != nil || d == nil || d.PlaybackStorage.ID != bound || d.AccountType != "binding" {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
			wantPick := "bound-pick"
			if sourceBound {
				wantPick = "source-pick"
			}
			if d.ActualPickCode != wantPick {
				t.Fatalf("pickcode=%s, want %s", d.ActualPickCode, wantPick)
			}
		}
	}
}

func TestBoundPlaybackEnforcesConcurrencyForSourceAndTarget(t *testing.T) {
	for _, sourceBound := range []bool{false, true} {
		db := setupBalanceServiceTestDB(t)
		source, target, match := createBalanceCacheBase(t, db, true)
		bound := &target
		if sourceBound {
			bound = &source
		}
		bound.Match302MaxActive = 1
		if err := db.Save(bound).Error; err != nil {
			t.Fatal(err)
		}
		match.CloudStorage = &source
		embyplayback.Default().Clear()
		t.Cleanup(func() { embyplayback.Default().Clear() })
		embyplayback.Default().AttachRedirect(embyproxylog.Entry{ItemID: "other-playback", ActualStorageID: bound.ID})
		d, err := NewBalanceAssignmentService(nil).ResolvePlayback(context.Background(), BalancePlaybackRequest{Match: &match, EmbyItemID: "new-playback", ForcedStorageID: bound.ID})
		if d != nil || err == nil || !strings.Contains(err.Error(), "并发已满") {
			t.Fatalf("decision=%+v err=%v", d, err)
		}
	}
}

func TestBoundPlaybackReportsTransferPendingAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		db := setupBalanceServiceTestDB(t)
		source, target, match := createBalanceCacheBase(t, db, false)
		source.Match302AccessMode = model.Match302AccessModeCookieOnly
		match.CloudStorage = &source
		a := model.Match302BalanceAssignment{Match302ID: match.ID, SourceFilePath: "/media/source/Test.mkv", SourceStorageID: source.ID, PlaybackStorageID: target.ID, Forced: true, Status: model.BalanceAssignmentStatusTransferring, SourcePickcode: "source-pick", SHA1: "ABC", Size: 123}
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		if fail {
			done := make(chan error, 1)
			go func() {
				time.Sleep(100 * time.Millisecond)
				done <- db.Model(&a).Updates(map[string]any{"status": model.BalanceAssignmentStatusFailed, "last_error": "目标账号空间不足"}).Error
			}()
			t.Cleanup(func() {
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
		}
		svc := NewBalanceAssignmentService(nil)
		svc.sourceFile115Svc = &fakeBalanceSourceFileResolver{file: Web115File{FileID: "file", PickCode: "source-pick", IsFile: true}, found: true}
		d, err := svc.ResolvePlayback(context.Background(), BalancePlaybackRequest{Match: &match, SourcePath: a.SourceFilePath, MatchedPath: "/library/Test.mkv", ForcedStorageID: target.ID})
		want := "等待 3 秒仍未就绪"
		if fail {
			want = "目标账号空间不足"
		}
		if d != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("decision=%+v err=%v, want %s", d, err, want)
		}
		var normal int64
		if err := db.Model(&model.Match302BalanceAssignment{}).Where("forced = ?", false).Count(&normal).Error; err != nil || normal != 0 {
			t.Fatalf("created ordinary assignment: count=%d err=%v", normal, err)
		}
	}
}
