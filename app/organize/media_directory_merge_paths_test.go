package organize

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestMergedDirectoryPreviewKeepsMountOutOf115Target(t *testing.T) {
	e := setupOrganizePlanTest(t)
	e.sourceName = "Kingdom.of.Heaven.2005.2160p.BluRay.mkv"
	e.req.MediaType = "movie"
	e.req.Category = "欧美电影"

	savePath := filepath.Join(t.TempDir(), "115LongTerm")
	const contentPrefix = "/CloudNAS/CloudDrive/115LongTerm"
	dir := model.MediaDirectory{ID: e.req.CloudDirectoryID}
	if err := e.db.Model(&dir).Updates(map[string]any{
		"directory_name": "影视中心", "source_path": "/115LongTerm/影视中心",
		"source_type": model.SourceTypeCloudDrive2, "mapping_enabled": true,
		"mapping_path_prefix": "/115LongTerm", "save_path": savePath, "content_prefix": contentPrefix,
	}).Error; err != nil {
		t.Fatal(err)
	}

	preview, err := e.handler.buildOrganize115CookieResult(1, e.req)
	if err != nil || len(preview.Items) != 1 {
		t.Fatalf("preview: %+v, err=%v", preview, err)
	}
	item := preview.Items[0]
	if item.Error != "" || !strings.HasPrefix(item.TargetPath, "/影视中心/欧美电影/") {
		t.Fatalf("wrong 115 target: %+v", item)
	}
	if !strings.HasPrefix(item.StrmPath, filepath.Join(savePath, "影视中心", "欧美电影")+string(filepath.Separator)) ||
		item.StrmContent != contentPrefix+item.TargetPath || e.mutations != 0 {
		t.Fatalf("wrong STRM preview: %+v, mutations=%d", item, e.mutations)
	}
}

func TestCloudDriveOrganizePreviewUsesVerified115Directory(t *testing.T) {
	for _, realRoot := range []string{"/影视中心", "/115LongTerm/影视中心", "/不同目录"} {
		t.Run(realRoot, func(t *testing.T) {
			e := setupOrganizePlanTest(t)
			e.sourceName = "Kingdom.of.Heaven.2005.2160p.BluRay.mkv"
			e.req.MediaType, e.req.Category = "movie", "欧美电影"
			const (
				sourcePath    = "/115LongTerm/影视中心"
				contentPrefix = "/CloudNAS/CloudDrive"
				selectedCID   = "123456"
			)
			savePath := t.TempDir()
			dir := model.MediaDirectory{ID: e.req.CloudDirectoryID}
			if err := e.db.Model(&dir).Updates(map[string]any{
				"directory_name": sourcePath, "directory_id": selectedCID,
				"source_path": sourcePath, "source_type": model.SourceTypeCloudDrive2,
				"mapping_enabled": true, "save_path": savePath, "content_prefix": contentPrefix,
			}).Error; err != nil {
				t.Fatal(err)
			}
			e.resolveTargetCID = func(lookup string) string {
				if "/"+lookup == realRoot {
					return selectedCID
				}
				return "900"
			}

			preview, err := e.handler.buildOrganize115CookieResult(1, e.req)
			if realRoot == "/不同目录" {
				if err == nil || !strings.Contains(err.Error(), "与所选 115 目录不一致") {
					t.Fatalf("unverified organize root accepted: %v", err)
				}
				return
			}
			if err != nil || len(preview.Items) != 1 {
				t.Fatalf("preview: %+v, err=%v", preview, err)
			}
			item := preview.Items[0]
			if item.Error != "" || !strings.HasPrefix(item.TargetPath, realRoot+"/欧美电影/") {
				t.Fatalf("wrong 115 target: %+v", item)
			}
			if !strings.HasPrefix(item.StrmPath, filepath.Join(savePath, sourcePath, "欧美电影")+string(filepath.Separator)) ||
				!strings.HasPrefix(item.StrmContent, contentPrefix+sourcePath+"/欧美电影/") || e.mutations != 0 {
				t.Fatalf("wrong STRM preview: %+v, mutations=%d", item, e.mutations)
			}
			if realRoot == sourcePath {
				return
			}

			// 模拟修复前的预览快照，确认其不能沿用旧的云端路径执行。
			if err := e.db.First(&dir, dir.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := e.db.Model(&model.OrganizePlan{}).Where("id = ?", preview.PlanID).
				Update("directory_json", organizeDirectoryConditions(dir)).Error; err != nil {
				t.Fatal(err)
			}
			req := e.req
			req.PlanID, req.DryRun = preview.PlanID, false
			if _, err := e.handler.buildOrganize115CookieResult(1, req); err == nil || !strings.Contains(err.Error(), "重新预览") || e.mutations != 0 {
				t.Fatalf("old cloud path was accepted: err=%v, mutations=%d", err, e.mutations)
			}

			preview, err = e.handler.buildOrganize115CookieResult(1, e.req)
			if err != nil {
				t.Fatal(err)
			}
			req.PlanID = preview.PlanID
			applied, err := e.handler.buildOrganize115CookieResult(1, req)
			if err != nil || len(applied.Groups) != 1 || applied.Groups[0].Error != "" || e.mutations != 2 {
				t.Fatalf("corrected plan failed: %+v, mutations=%d, err=%v", applied, e.mutations, err)
			}
			if content, err := os.ReadFile(item.StrmPath); err != nil || string(content) != item.StrmContent {
				t.Fatalf("STRM content = %q, err=%v", content, err)
			}
		})
	}
}

func TestMergedDirectoryKeepsOrganizeAndUpgradeBindingPaths(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		root := t.TempDir()
		dir := model.MediaDirectory{ID: 8, DirectoryName: "影视中心", SavePath: filepath.Join(root, "115LongTerm"), ContentPrefix: "/CloudNAS/CloudDrive/115LongTerm", ContentEncodeURI: encoded}
		mapping := model.MediaDirectory{ID: 9, MappingEnabled: true, SourceType: model.SourceTypeCloudDrive2, SourcePath: "/115LongTerm/影视中心", SavePath: root, ContentPrefix: "/CloudNAS/CloudDrive", ContentEncodeURI: encoded, FilterRules: `{"include":[".mkv"],"download":[".srt"]}`}
		merged := dir
		merged.MappingEnabled = true
		merged.OrganizeEnabled = true
		merged.MappingPathPrefix = "/115LongTerm"
		merged.SourcePath = mapping.SourcePath
		merged.SourceType = mapping.SourceType
		merged.FilterRules = mapping.FilterRules
		cloud := "/影视中心/电影/样本 #1.mkv"
		a, b := buildStrmInfo(dir.SavePath, dir.ContentPrefix, cloud, encoded)
		c, d := buildStrmInfo(merged.SavePath, merged.ContentPrefix, cloud, encoded)
		if a != c || b != d {
			t.Fatal("organizing output changed")
		}
		before := upgradeBindings(dir, []model.MediaDirectory{mapping}, cloud)
		after := upgradeBindings(merged, []model.MediaDirectory{merged}, cloud)
		if len(before) != 1 || !reflect.DeepEqual(before, after) {
			t.Fatalf("upgrade bindings changed: %+v %+v", before, after)
		}
	}
}
