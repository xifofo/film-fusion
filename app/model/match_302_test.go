package model

import "testing"

func TestMatch302GetMatchedPathWithEmptyTargetPath(t *testing.T) {
	match := Match302{
		SourcePath: "/media/source",
		TargetPath: "",
	}

	got := match.GetMatchedPath("/media/source/Movie/test.mkv")
	want := "/Movie/test.mkv"
	if got != want {
		t.Fatalf("GetMatchedPath() = %q, want %q", got, want)
	}
}

func TestMatch302GetMatchedPathWithNonEmptyTargetPath(t *testing.T) {
	match := Match302{
		SourcePath: "/media/source",
		TargetPath: "/library",
	}

	got := match.GetMatchedPath("/media/source/Movie/test.mkv")
	want := "/library/Movie/test.mkv"
	if got != want {
		t.Fatalf("GetMatchedPath() = %q, want %q", got, want)
	}
}
