package service

import (
	"strings"
	"testing"
)

func TestApplyMediaRecognitionWordsSupportsMoviePilotFormats(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		words   []string
		want    string
		applied int
	}{
		{
			name:  "block word",
			input: "Show.REPACK.S01E01.mkv",
			words: []string{"REPACK\\.?"},
			want:  "Show.S01E01.mkv", applied: 1,
		},
		{
			name:  "replacement with python backreference",
			input: "Show.01.mkv",
			words: []string{`Show\.(\d+) => Example.S01E\1`},
			want:  "Example.S01E01.mkv", applied: 1,
		},
		{
			name:  "replacement can delete a word",
			input: "Show.REPACK.S01E01.mkv",
			words: []string{"REPACK. => "},
			want:  "Show.S01E01.mkv", applied: 1,
		},
		{
			name:  "episode offset keeps zero padding",
			input: "动画 第03集",
			words: []string{"第 <> 集 >> EP+66"},
			want:  "动画 第69集", applied: 1,
		},
		{
			name:  "chinese episode offset",
			input: "动画 第三集",
			words: []string{"第 <> 集 >> EP+1"},
			want:  "动画 第四集", applied: 1,
		},
		{
			name:  "combined replacement then offset",
			input: "旧名 第03集",
			words: []string{"旧名 => 新名 && 第 <> 集 >> 2*EP-1"},
			want:  "新名 第05集", applied: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ApplyMediaRecognitionWords(test.input, test.words)
			if result.Processed != test.want {
				t.Fatalf("processed=%q want=%q steps=%#v", result.Processed, test.want, result.Steps)
			}
			if len(result.AppliedWords) != test.applied {
				t.Fatalf("applied=%#v", result.AppliedWords)
			}
		})
	}
}

func TestValidateMediaRecognitionWordsRejectsUnsafeOrAmbiguousRules(t *testing.T) {
	tests := []struct {
		name  string
		words []string
		want  string
	}{
		{name: "operator spaces", words: []string{"旧名=>新名"}, want: "运算符两侧"},
		{name: "invalid regex", words: []string{"[ => 新名"}, want: "正则表达式无效"},
		{name: "implicit EP multiplication", words: []string{"第 <> 集 >> 2EP"}, want: "不能省略运算符"},
		{name: "division by zero", words: []string{"第 <> 集 >> EP/0"}, want: "除以 0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateMediaRecognitionWords(test.words)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestApplyMediaRecognitionWordsRecordsNonMatchingRules(t *testing.T) {
	result := ApplyMediaRecognitionWords("Show.S01E01", []string{
		"# 保留注释",
		"Other => Example",
		"Show => Series",
	})
	if result.Processed != "Series.S01E01" {
		t.Fatalf("processed=%q", result.Processed)
	}
	if len(result.Steps) != 2 || result.Steps[0].Applied || !result.Steps[1].Applied {
		t.Fatalf("steps=%#v", result.Steps)
	}
}

func TestCombinedWordKeepsReplacementWithoutRecordingMissingOffset(t *testing.T) {
	result := ApplyMediaRecognitionWords("旧名 特别篇", []string{
		"旧名 => 新名 && 第 <> 集 >> EP+1",
	})
	if result.Processed != "新名 特别篇" {
		t.Fatalf("processed=%q", result.Processed)
	}
	if len(result.AppliedWords) != 0 || len(result.Steps) != 1 || result.Steps[0].Applied {
		t.Fatalf("result=%#v", result)
	}
}
