// 验证目录操作只作废未执行的预览，并保留任务快照和恢复保护。
package handler

import (
	"fmt"
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestMediaDirectoryTaskLifecycle(t *testing.T) {
	cases := []struct {
		name    string
		plan    string
		preview string
		upgrade string
		blocked bool
		label   string
		status  string
	}{
		{name: "executed", plan: "executed"},
		{name: "ordinary failure", plan: "failed"},
		{name: "completed upgrade", plan: "executed", upgrade: "completed"},
		{name: "restored upgrade", plan: "failed", upgrade: "rolled_back"},
		{name: "failed preview", preview: "failed"},
		{name: "completed preview", preview: "completed"},
		{name: "legacy applied", plan: "applied"},
		{name: "legacy completed", plan: "completed"},
		{name: "cancelled", plan: "cancelled", preview: "cancelled", upgrade: "cancelled"},
		{name: "canceled", plan: "canceled", preview: "canceled", upgrade: "canceled"},
		{name: "ready plan", plan: "ready"},
		{name: "ready plan with active recovery", plan: "ready", upgrade: "failed", blocked: true, label: "洗版恢复任务 #task-1", status: "failed"},
		{name: "executing plan", plan: "executing", blocked: true, label: "整理计划 #task-1", status: "executing"},
		{name: "queued preview", preview: "pending", blocked: true, label: "预整理任务 #31", status: "pending"},
		{name: "running preview", preview: "processing", blocked: true, label: "预整理任务 #31", status: "processing"},
		{name: "pending recovery", plan: "failed", upgrade: "pending", blocked: true, label: "洗版恢复任务 #task-1", status: "pending"},
		{name: "running recovery", plan: "executed", upgrade: "running", blocked: true, label: "洗版恢复任务 #task-1", status: "running"},
		{name: "failed recovery", plan: "failed", upgrade: "failed", blocked: true, label: "洗版恢复任务 #task-1", status: "failed"},
		{name: "interrupted upgrade", plan: "executing", upgrade: "failed", blocked: true, label: "洗版恢复任务 #task-1", status: "failed"},
		{name: "unknown plan", plan: "unknown", blocked: true, label: "整理计划 #task-1", status: "unknown"},
		{name: "unknown preview", preview: "unknown", blocked: true, label: "预整理任务 #31", status: "unknown"},
		{name: "unknown recovery", upgrade: "unknown", blocked: true, label: "洗版恢复任务 #task-1", status: "unknown"},
	}
	for _, operation := range []string{"delete", "disable", "merge-preview", "merge", "merge-source"} {
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				db, router, target, mapping := mergeDirectoryFixture(t)
				if err := db.AutoMigrate(&model.OrganizePreviewTask{}); err != nil {
					t.Fatal(err)
				}

				// 先取得版本再插入任务，验证最终合并也重新检查，不只在预览时检查。
				p := mergePreviewRequest(t, router, target.ID, mapping.ID)
				directoryID := target.ID
				if operation == "merge-source" {
					directoryID = mapping.ID
				}
				if operation == "disable" {
					// 保留同步用途，使清空整理目录 ID 后仍是一条有效配置。
					if err := db.Model(&target).Updates(map[string]any{
						"mapping_enabled": true,
						"source_path":     "/115LongTerm/另一目录", "source_type": mapping.SourceType,
						"link_type": mapping.LinkType, "strm_content_type": mapping.StrmContentType,
					}).Error; err != nil {
						t.Fatal(err)
					}
				}

				rows := []any{}
				if tc.plan != "" {
					rows = append(rows, &model.OrganizePlan{ID: "task-1", UserID: 1, CloudDirectoryID: directoryID, Status: tc.plan, DirectoryJSON: `{"snapshot":"unchanged"}`})
				}
				if tc.preview != "" {
					rows = append(rows, &model.OrganizePreviewTask{ID: 31, UserID: 1, CloudDirectoryID: directoryID, Status: model.OrganizePreviewTaskStatus(tc.preview)})
				}
				if tc.upgrade != "" {
					rows = append(rows, &model.OrganizeUpgradeJob{ID: "task-1", UserID: 1, CloudDirectoryID: directoryID, Status: tc.upgrade, PayloadJSON: `{"snapshot":"unchanged"}`})
				}
				for _, row := range rows {
					if err := db.Create(row).Error; err != nil {
						t.Fatal(err)
					}
				}
				// 另一个目录的草稿不受影响。
				unrelated := model.OrganizePlan{ID: "unrelated", UserID: 1, CloudDirectoryID: 99, Status: "ready"}
				if err := db.Create(&unrelated).Error; err != nil {
					t.Fatal(err)
				}

				method, url, body := "DELETE", fmt.Sprintf("/media-directories/%d", target.ID), ""
				switch operation {
				case "disable":
					method, body = "PUT", `{"directory_id":""}`
				case "merge-preview":
					method, url = "POST", url+"/merge-preview"
					body = fmt.Sprintf(`{"mapping_id":%d}`, mapping.ID)
				case "merge", "merge-source":
					method, url = "POST", url+"/merge"
					body = fmt.Sprintf(`{"mapping_id":%d,"revision":%q}`, mapping.ID, p.Revision)
				}
				response := directoryRequest(router, method, url, body)
				want := 200
				if tc.blocked {
					want = 409
				}
				if response.Code != want {
					t.Fatalf("status=%d want=%d: %s", response.Code, want, response.Body)
				}
				if tc.blocked {
					for _, part := range []string{fmt.Sprintf("媒体目录 #%d", directoryID), tc.label, "状态：" + tc.status} {
						if !strings.Contains(response.Body.String(), part) {
							t.Fatalf("missing task location %q: %s", part, response.Body)
						}
					}
					var count int64
					if err := db.Model(&model.MediaDirectory{}).Where("id IN ?", []uint{target.ID, mapping.ID}).Count(&count).Error; err != nil || count != 2 {
						t.Fatalf("blocked operation removed directory: count=%d err=%v", count, err)
					}
				}

				// 只有实际变更目录才会作废待执行计划。
				if tc.plan != "" {
					wantStatus := tc.plan
					if wantStatus == "ready" && !tc.blocked && operation != "merge-preview" {
						wantStatus = "cancelled"
					}
					var saved model.OrganizePlan
					if err := db.First(&saved, "id = ?", "task-1").Error; err != nil || saved.Status != wantStatus || saved.CloudDirectoryID != directoryID || saved.DirectoryJSON != `{"snapshot":"unchanged"}` {
						t.Fatalf("plan changed: %+v err=%v", saved, err)
					}
				}
				if err := db.First(&unrelated, "id = ?", unrelated.ID).Error; err != nil || unrelated.Status != "ready" {
					t.Fatalf("unrelated plan changed: %+v err=%v", unrelated, err)
				}
				if tc.upgrade != "" {
					var saved model.OrganizeUpgradeJob
					if err := db.First(&saved, "id = ?", "task-1").Error; err != nil || saved.Status != tc.upgrade || saved.CloudDirectoryID != directoryID || saved.PayloadJSON != `{"snapshot":"unchanged"}` {
						t.Fatalf("recovery changed: %+v err=%v", saved, err)
					}
				}
			})
		}
	}
}
