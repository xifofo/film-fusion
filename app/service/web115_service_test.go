package service

import (
	"errors"
	"reflect"
	"testing"
)

func TestResolveWeb115FilePathUsesExactParentAndFileName(t *testing.T) {
	var gotParent string
	file, found, err := resolveWeb115FilePath(
		"/影视中心/Movie%20Name/Test.mkv",
		func(parent string) (string, bool, error) {
			gotParent = parent
			return "parent-id", true, nil
		},
		func(cid string, offset, limit int) (Web115ListResult, error) {
			if cid != "parent-id" || offset != 0 || limit != 1150 {
				t.Fatalf("unexpected list request cid=%q offset=%d limit=%d", cid, offset, limit)
			}
			return Web115ListResult{Items: []Web115File{
				{Name: "Other.mkv", IsFile: true, PickCode: "other"},
				{Name: "Test.mkv", IsFile: false, FileID: "directory"},
				{Name: "Test.mkv", IsFile: true, FileID: "file-id", PickCode: "pick-code", SHA1: "sha1", Size: 123},
			}, Total: 3}, nil
		},
	)
	if err != nil {
		t.Fatalf("resolveWeb115FilePath returned error: %v", err)
	}
	if !found {
		t.Fatal("resolveWeb115FilePath did not find file")
	}
	if gotParent != "/影视中心/Movie Name" {
		t.Fatalf("parent path = %q", gotParent)
	}
	if file.FileID != "file-id" || file.PickCode != "pick-code" || file.Size != 123 {
		t.Fatalf("resolved file = %+v", file)
	}
}

func TestResolveWeb115FilePathPaginates(t *testing.T) {
	var offsets []int
	file, found, err := resolveWeb115FilePath(
		"/library/Test.mkv",
		func(string) (string, bool, error) { return "parent-id", true, nil },
		func(_ string, offset, _ int) (Web115ListResult, error) {
			offsets = append(offsets, offset)
			if offset == 0 {
				return Web115ListResult{Items: []Web115File{{Name: "Other.mkv", IsFile: true}}, Total: 1151}, nil
			}
			return Web115ListResult{Items: []Web115File{{Name: "Test.mkv", IsFile: true, PickCode: "pick"}}, Total: 1151}, nil
		},
	)
	if err != nil || !found || file.PickCode != "pick" {
		t.Fatalf("resolve result file=%+v found=%v err=%v", file, found, err)
	}
	if !reflect.DeepEqual(offsets, []int{0, 1150}) {
		t.Fatalf("offsets = %v", offsets)
	}
}

func TestResolveWeb115FilePathReturnsMissWithoutListingWhenParentMissing(t *testing.T) {
	listCalled := false
	_, found, err := resolveWeb115FilePath(
		"/missing/Test.mkv",
		func(string) (string, bool, error) { return "", false, nil },
		func(string, int, int) (Web115ListResult, error) {
			listCalled = true
			return Web115ListResult{}, nil
		},
	)
	if err != nil || found || listCalled {
		t.Fatalf("found=%v listCalled=%v err=%v", found, listCalled, err)
	}
}

func TestResolveWeb115FilePathWrapsLookupErrors(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	_, _, err := resolveWeb115FilePath(
		"/library/Test.mkv",
		func(string) (string, bool, error) { return "", false, lookupErr },
		func(string, int, int) (Web115ListResult, error) { return Web115ListResult{}, nil },
	)
	if err == nil || !errors.Is(err, lookupErr) {
		t.Fatalf("error = %v", err)
	}
}
