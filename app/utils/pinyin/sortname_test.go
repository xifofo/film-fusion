package pinyin

import "testing"

func TestToSortName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"纯中文", "流浪地球", "LLDQ"},
		{"中英混排", "复仇者联盟4：终局之战", "FCZLM4ZJZZ"},
		{"英文 The 前缀", "The Matrix", "MATRIX"},
		{"英文 A 前缀", "A Quiet Place", "QUIETPLACE"},
		{"英文 An 前缀", "An American Tail", "AMERICANTAIL"},
		{"全英文", "Inception", "INCEPTION"},
		{"数字开头", "1917", "#1917"},
		{"中文+数字", "007之黄金眼", "#007ZHJY"},
		{"空字符串", "", "#"},
		{"全符号", "!!!???", "#"},
		{"含空格的英文", "Star Wars Episode IV", "STARWARSEPISODEIV"},
		{"前后空白", "  你好世界  ", "NHSJ"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToSortName(tc.in)
			if got != tc.want {
				t.Errorf("ToSortName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
