package organize

import (
	"os"
	"path/filepath"
	"testing"

	"film-fusion/app/model"
)

// 同步目录单独存在时，整理目标、本地文件和播放内容必须与原纯整理配置一致。
func TestSingleSyncDirectoryReplacesOrganizeDirectory(t *testing.T) {
	for _, name := range []string{"/115LongTerm/影视中心", "影视中心", "我的媒体库"} {
		t.Run(name, func(t *testing.T) {
			e := setupOrganizePlanTest(t)
			e.sourceName = "Kingdom.of.Heaven.2005.2160p.BluRay.mkv"
			e.req.MediaType, e.req.Category = "movie", "欧美电影"
			root := t.TempDir()
			const cid = "123456"
			e.resolveTargetCID = func(lookup string) string {
				if lookup == "影视中心" {
					return cid
				}
				return "900"
			}
			var original model.MediaDirectory
			if err := e.db.First(&original, e.req.CloudDirectoryID).Error; err != nil {
				t.Fatal(err)
			}
			original.DirectoryName, original.DirectoryID = "影视中心", cid
			original.SavePath = filepath.Join(root, "115LongTerm")
			original.ContentPrefix = "/CloudNAS/CloudDrive/115LongTerm"
			if err := e.db.Save(&original).Error; err != nil {
				t.Fatal(err)
			}
			before, err := e.handler.buildOrganize115CookieResult(1, e.req)
			if err != nil || len(before.Items) != 1 || before.Items[0].Error != "" {
				t.Fatalf("original preview: %+v, err=%v", before, err)
			}

			// 新配置直接使用同步的保存根，无需合并前缀或读取原整理记录。
			single := original
			single.ID = 0
			single.DirectoryName = name
			single.MappingEnabled = true
			single.SourceType = model.SourceTypeCloudDrive2
			single.SourcePath = "/115LongTerm/影视中心"
			single.SavePath, single.ContentPrefix = root, "/CloudNAS/CloudDrive"
			if err := e.db.Create(&single).Error; err != nil {
				t.Fatal(err)
			}
			if err := e.db.Delete(&original).Error; err != nil {
				t.Fatal(err)
			}
			e.req.CloudDirectoryID = single.ID
			after, err := e.handler.buildOrganize115CookieResult(1, e.req)
			if err != nil || len(after.Items) != 1 || after.Items[0].Error != "" {
				t.Fatalf("single config preview: %+v, err=%v", after, err)
			}
			a, b := before.Items[0], after.Items[0]
			if a.TargetPath != b.TargetPath || a.StrmPath != b.StrmPath || a.StrmContent != b.StrmContent || e.mutations != 0 {
				t.Fatalf("paths differ: before=%+v, after=%+v, mutations=%d", a, b, e.mutations)
			}

			e.req.PlanID, e.req.DryRun = after.PlanID, false
			applied, err := e.handler.buildOrganize115CookieResult(1, e.req)
			if err != nil || len(applied.Groups) != 1 || applied.Groups[0].Error != "" {
				t.Fatalf("single config execution: %+v, err=%v", applied, err)
			}
			if content, err := os.ReadFile(b.StrmPath); err != nil || string(content) != b.StrmContent {
				t.Fatalf("STRM content=%q, err=%v", content, err)
			}
			var saved model.MediaDirectory
			if err := e.db.First(&saved, single.ID).Error; err != nil || saved.SavePath != root || saved.DirectoryName != name || saved.MappingPathPrefix != "" {
				t.Fatalf("runtime config leaked into saved configuration: %+v, err=%v", saved, err)
			}
		})
	}
}
