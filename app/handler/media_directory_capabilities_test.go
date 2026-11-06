// 通过目录 API 验证自动用途、增量清空、校验边界与被引用目录的事务回滚。
package handler

import (
	"encoding/json"
	"fmt"
	"testing"

	"film-fusion/app/model"
)

func TestMediaDirectoryDerivesCapabilitiesFromConfiguration(t *testing.T) {
	for _, test := range []struct {
		name     string
		fields   string
		status   int
		mapping  bool
		organize bool
	}{
		{"sync only", `"source_path":"  /Movies  ","directory_id":" ","recognition_source":"unused","exclude_smaller_than_mb":-1`, 200, true, false},
		{"organization only with root ID", `"directory_id":" 0 ","source_path":" ","source_type":"","filter_rules":"unused draft"`, 200, false, true},
		{"both ignore false flags", `"source_path":"/Movies","directory_id":"123","mapping_enabled":false,"organize_enabled":false`, 200, true, true},
		{"empty configuration", `"source_type":"moviepilot2","save_path":"/strm"`, 400, false, false},
		{"true flags cannot enable empty configuration", `"mapping_enabled":true,"organize_enabled":true,"source_path":" ","directory_id":" "`, 400, false, false},
		{"incomplete sync source", `"source_path":"/Movies","source_type":"","directory_id":"123"`, 400, false, false},
		{"invalid sync rules", `"source_path":"/Movies","filter_rules":"broken"`, 400, false, false},
		{"invalid organization engine", `"directory_id":"123","recognition_source":"unknown-engine"`, 400, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, router := mediaDirectoryFixture(t)
			body := `{"cloud_storage_id":1,"directory_name":"Configured",` + test.fields + `}`
			res := directoryRequest(router, "POST", "/media-directories", body)
			if res.Code != test.status {
				t.Fatalf("status %d: %s", res.Code, res.Body)
			}

			var rows []model.MediaDirectory
			if err := db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if test.status != 200 {
				if len(rows) != 0 {
					t.Fatal("invalid configuration was persisted")
				}
				return
			}

			var envelope struct{ Data model.MediaDirectory }
			if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].MappingEnabled != test.mapping || rows[0].OrganizeEnabled != test.organize ||
				envelope.Data.MappingEnabled != test.mapping || envelope.Data.OrganizeEnabled != test.organize {
				t.Fatalf("wrong derived capabilities: %s", res.Body)
			}
			if test.mapping && rows[0].SourcePath != "/Movies" || test.name == "organization only with root ID" && rows[0].DirectoryID != "0" {
				t.Fatal("key configuration was not trimmed correctly")
			}
		})
	}
}

func TestMediaDirectoryPartialUpdatesRecomputeCapabilities(t *testing.T) {
	db, router := mediaDirectoryFixture(t)
	res := directoryRequest(router, "POST", "/media-directories", `{"cloud_storage_id":1,"directory_name":"Both","source_path":"/Movies","directory_id":"123","save_path":"/strm"}`)
	if res.Code != 200 {
		t.Fatalf("create: %s", res.Body)
	}
	var envelope struct{ Data model.MediaDirectory }
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/media-directories/%d", envelope.Data.ID)

	for _, step := range []struct {
		patch    string
		status   int
		mapping  bool
		organize bool
	}{
		{`{"source_path":" "}`, 200, false, true},
		{`{"mapping_enabled":true,"organize_enabled":false}`, 200, false, true},
		{`{"source_path":"/Movies","directory_id":" "}`, 200, true, false},
		{`{"source_path":""}`, 400, true, false},
		{`{"directory_id":"0"}`, 200, true, true},
	} {
		res := directoryRequest(router, "PUT", url, step.patch)
		if res.Code != step.status {
			t.Fatalf("patch %s: %d %s", step.patch, res.Code, res.Body)
		}
		var row model.MediaDirectory
		if err := db.First(&row, envelope.Data.ID).Error; err != nil {
			t.Fatal(err)
		}
		if row.MappingEnabled != step.mapping || row.OrganizeEnabled != step.organize || row.SavePath != "/strm" {
			t.Fatalf("patch %s changed unrelated settings or persisted wrong capabilities", step.patch)
		}
	}
}

func TestMediaDirectoryCannotClearReferencedOrganization(t *testing.T) {
	db, router := mediaDirectoryFixture(t)
	row := model.MediaDirectory{ID: 8, UserID: 1, CloudStorageID: 1, DirectoryName: "used", MappingEnabled: true, OrganizeEnabled: true, DirectoryID: "123", SourcePath: "/Movies", SourceType: model.SourceTypeMoviePilot2, LinkType: model.LinkTypeStrm}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE automation_workflows (definition_json TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO automation_workflows (definition_json) VALUES (?)", `{"cloud_directory_id":8}`).Error; err != nil {
		t.Fatal(err)
	}

	res := directoryRequest(router, "PUT", "/media-directories/8", `{"directory_id":""}`)
	if res.Code != 409 {
		t.Fatalf("used organization cleared: %d %s", res.Code, res.Body)
	}
	var saved model.MediaDirectory
	if err := db.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.DirectoryID != "123" || !saved.OrganizeEnabled {
		t.Fatal("rejected change was not rolled back")
	}
}

func TestMediaDirectoryCanClearUnreferencedCapability(t *testing.T) {
	for _, test := range []struct {
		name  string
		patch string
		pluto bool
	}{
		{"keep referenced sync", `{"directory_id":""}`, true},
		{"keep referenced organization", `{"source_path":""}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, router := mediaDirectoryFixture(t)
			row := model.MediaDirectory{ID: 8, UserID: 1, CloudStorageID: 1, DirectoryName: "used", MappingEnabled: true, OrganizeEnabled: true, DirectoryID: "123", SourcePath: "/Movies", SourceType: model.SourceTypeMoviePilot2, LinkType: model.LinkTypeStrm}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			if test.pluto {
				if err := db.Create(&model.SystemConfig{ConfigKey: "plutofish.settings", ConfigValue: `{"userID":1,"config":{"mappings":[{"cloudPathID":8}]}}`}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Exec("CREATE TABLE automation_workflows (definition_json TEXT)").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("INSERT INTO automation_workflows (definition_json) VALUES (?)", `{"cloud_directory_id":8}`).Error; err != nil {
					t.Fatal(err)
				}
			}

			res := directoryRequest(router, "PUT", "/media-directories/8", test.patch)
			if res.Code != 200 {
				t.Fatalf("unrelated reference blocked patch: %d %s", res.Code, res.Body)
			}
			var saved model.MediaDirectory
			if err := db.First(&saved, row.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.MappingEnabled != test.pluto || saved.OrganizeEnabled == test.pluto {
				t.Fatal("retained referenced capability was changed")
			}
		})
	}
}
