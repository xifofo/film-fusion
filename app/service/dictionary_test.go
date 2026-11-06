// 验证稳定值、条件写入、候选过滤及数据库约束，不连接外部网盘。
package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func dictionaryTestService(t *testing.T) *DictionaryService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&model.Dictionary{}, &model.DictionaryItem{}, &model.CloudStorage{}); err != nil {
		t.Fatal(err)
	}
	return NewDictionaryService(db)
}

func dictionaryTestPtr[T any](v T) *T { return &v }

func TestDictionaryCRUDAndVersions(t *testing.T) {
	s := dictionaryTestService(t)
	ctx := context.Background()
	row, err := s.Create(ctx, DictionaryInput{Code: "sample.status", Name: "Status", Description: "clear me", Enabled: dictionaryTestPtr(false)})
	if err != nil || row.Enabled || row.Version != 1 {
		t.Fatalf("create=%+v err=%v", row, err)
	}
	if _, err := s.Create(ctx, DictionaryInput{Code: row.Code, Name: "duplicate"}); !errors.Is(err, ErrDictionaryExists) {
		t.Fatalf("duplicate=%v", err)
	}
	changed, err := s.Update(ctx, row.Code, DictionaryPatch{ExpectedVersion: 1, Enabled: dictionaryTestPtr(true), Description: dictionaryTestPtr("")})
	if err != nil || !changed.Enabled || changed.Version != 2 || changed.Description != "" {
		t.Fatalf("update=%+v err=%v", changed, err)
	}
	if _, err := s.Update(ctx, row.Code, DictionaryPatch{ExpectedVersion: 1, Name: dictionaryTestPtr("stale")}); !errors.Is(err, ErrDictionaryVersion) {
		t.Fatalf("stale update=%v", err)
	}
	item, err := s.CreateItem(ctx, row.Code, DictionaryItemInput{Value: "001", Label: "Original", SortOrder: 2, Description: "clear"})
	if err != nil || item.Value != "001" || !item.Enabled || item.Version != 1 {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	if _, err := s.CreateItem(ctx, row.Code, DictionaryItemInput{Value: "001", Label: "duplicate"}); !errors.Is(err, ErrDictionaryExists) {
		t.Fatalf("duplicate item=%v", err)
	}
	updated, err := s.UpdateItem(ctx, row.Code, item.ID, DictionaryItemPatch{ExpectedVersion: 1, Label: dictionaryTestPtr("Edited"), SortOrder: dictionaryTestPtr(0), Enabled: dictionaryTestPtr(false), Description: dictionaryTestPtr("")})
	if err != nil || updated.Value != "001" || updated.Version != 2 || updated.Enabled || updated.SortOrder != 0 || updated.Description != "" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := s.UpdateItem(ctx, row.Code, item.ID, DictionaryItemPatch{ExpectedVersion: 1, Label: dictionaryTestPtr("stale")}); !errors.Is(err, ErrDictionaryVersion) {
		t.Fatalf("item conflict=%v", err)
	}
	if err := s.DeleteItem(ctx, row.Code, item.ID, 1); !errors.Is(err, ErrDictionaryVersion) {
		t.Fatalf("delete conflict=%v", err)
	}
	other, err := s.Create(ctx, DictionaryInput{Code: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetItem(ctx, other.Code, item.ID); !errors.Is(err, ErrDictionaryMissing) {
		t.Fatalf("cross-parent get=%v", err)
	}
	if err := s.DeleteItem(ctx, other.Code, item.ID, 2); !errors.Is(err, ErrDictionaryMissing) {
		t.Fatalf("cross-parent delete=%v", err)
	}
	if err := s.DeleteItem(ctx, row.Code, item.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetItem(ctx, row.Code, item.ID); !errors.Is(err, ErrDictionaryMissing) {
		t.Fatalf("deleted item=%v", err)
	}
}

func TestDictionarySearchSortingAndOptions(t *testing.T) {
	s := dictionaryTestService(t)
	ctx := context.Background()
	q := DictionaryQuery{Page: 1, PageSize: 1}
	_, err := s.Create(ctx, DictionaryInput{Code: "sample", Name: "Sample"})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []DictionaryItemInput{{Value: "a", Label: "Alpha", SortOrder: 20}, {Value: "b", Label: "Beta", SortOrder: -1}, {Value: "c", Label: "Hidden", Enabled: dictionaryTestPtr(false)}}
	for i, in := range inputs {
		item, err := s.CreateItem(ctx, "sample", in)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&item).Updates(map[string]any{"created_at": time.Unix(int64(100+i), 0), "updated_at": time.Unix(int64(100-i), 0)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	options, err := s.Options(ctx, "sample", q)
	if err != nil || options.Total != 2 || len(options.Items) != 1 || options.Items[0].Value != "b" {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	q.Page = 2
	options, err = s.Options(ctx, "sample", q)
	if err != nil || len(options.Items) != 1 || options.Items[0].Value != "a" {
		t.Fatalf("second page=%+v err=%v", options, err)
	}
	q.Page = 1
	q.Search = "ALPHA"
	options, err = s.Options(ctx, "sample", q)
	if err != nil || options.Total != 1 || options.Items[0].Value != "a" {
		t.Fatalf("search=%+v err=%v", options, err)
	}
	q.IDs = []string{"b", "c", "missing"}
	options, err = s.Options(ctx, "sample", q)
	if err != nil || options.Total != 1 || len(options.Items) != 1 || options.Items[0].Value != "b" {
		t.Fatalf("resolve must override search: %+v %v", options, err)
	}
	q = DictionaryQuery{Page: 1, PageSize: 1, Sort: "createdAt", Order: "desc"}
	items, err := s.Items(ctx, "sample", q)
	if err != nil || items.Total != 3 || items.Items[0].Value != "c" {
		t.Fatalf("date sort=%+v err=%v", items, err)
	}
	q.Enabled = dictionaryTestPtr(false)
	items, err = s.Items(ctx, "sample", q)
	if err != nil || items.Total != 1 || items.Items[0].Value != "c" {
		t.Fatalf("disabled filter=%+v err=%v", items, err)
	}
	q.Search = "%"
	items, err = s.Items(ctx, "sample", q)
	if err != nil || items.Total != 0 {
		t.Fatalf("search must be literal: %+v %v", items, err)
	}
	q = DictionaryQuery{Page: 1, PageSize: 20, Search: "SAMPLE"}
	types, err := s.List(ctx, q)
	if err != nil || types.Total != 1 {
		t.Fatalf("type search=%+v %v", types, err)
	}
	_, err = s.Update(ctx, "sample", DictionaryPatch{ExpectedVersion: 1, Enabled: dictionaryTestPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Options(ctx, "sample", q); !errors.Is(err, ErrDictionaryDisabled) {
		t.Fatalf("disabled options=%v", err)
	}
	if _, err := s.Items(ctx, "sample", DictionaryQuery{Page: 1, PageSize: 20}); err != nil {
		t.Fatal("disabled parent remains maintainable", err)
	}
}

func TestDictionaryValidationAndConcurrentWrite(t *testing.T) {
	s := dictionaryTestService(t)
	ctx := context.Background()
	for _, code := range []string{"", "CAPITAL", "bad code"} {
		if _, err := s.Create(ctx, DictionaryInput{Code: code, Name: "name"}); !errors.Is(err, ErrDictionaryInvalid) {
			t.Fatalf("invalid code %q: %v", code, err)
		}
	}
	_, err := s.Create(ctx, DictionaryInput{Code: "cloud.115_apps", Name: "Apps"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"001", "0", "abc", "123456789012345678901"} {
		if _, err := s.CreateItem(ctx, "cloud.115_apps", DictionaryItemInput{Value: value, Label: "App"}); !errors.Is(err, ErrDictionaryInvalid) {
			t.Fatalf("invalid client %q: %v", value, err)
		}
	}
	item, err := s.CreateItem(ctx, "cloud.115_apps", DictionaryItemInput{Value: "12345678901234567890", Label: "App"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateItem(ctx, "cloud.115_apps", item.ID, DictionaryItemPatch{ExpectedVersion: 1, Label: dictionaryTestPtr("Concurrent")})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrDictionaryVersion) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	for _, q := range []DictionaryQuery{{Page: 0, PageSize: 20}, {Page: 1, PageSize: 101}, {Page: 1, PageSize: 20, Sort: "created_at; DROP TABLE dictionaries"}, {Page: 1, PageSize: 20, Order: "random"}} {
		if _, err := s.List(ctx, q); !errors.Is(err, ErrDictionaryInvalid) {
			t.Fatalf("invalid query %+v: %v", q, err)
		}
	}
	// 目录项仅是候选；删除不能连带写入已有网盘的授权字段。
	storage := model.CloudStorage{UserID: 1, StorageType: "115", StorageName: "Existing", AppID: item.Value, AccessToken: "local-test-only"}
	if err := s.db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, "cloud.115_apps", item.ID, 2); err != nil {
		t.Fatal(err)
	}
	var actual model.CloudStorage
	if err := s.db.First(&actual, storage.ID).Error; err != nil {
		t.Fatal(err)
	}
	if actual.AppID != storage.AppID || actual.AccessToken != storage.AccessToken {
		t.Fatal("catalog delete altered existing authorization")
	}
}
