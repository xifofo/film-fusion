package organize

import (
	"path/filepath"
	"reflect"
	"testing"

	"film-fusion/app/model"
)

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
