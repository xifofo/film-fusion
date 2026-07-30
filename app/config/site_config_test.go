package config

import "testing"

func TestValidateSite(t *testing.T) {
	valid := SiteConfig{
		LoginTitle:         "我的媒体中心",
		LoginSubtitle:      "简单的媒体管理工具",
		LoginFormTitle:     "欢迎",
		LoginFormSubtitle:  "请登录管理后台",
		LoginBackgroundURL: "https://example.com/background.jpg",
	}
	if err := ValidateSite(valid); err != nil {
		t.Fatalf("expected valid site config, got %v", err)
	}

	blankTitle := valid
	blankTitle.LoginTitle = " "
	if err := ValidateSite(blankTitle); err == nil {
		t.Fatal("expected blank login title to be rejected")
	}

	blankSubtitle := valid
	blankSubtitle.LoginSubtitle = " "
	if err := ValidateSite(blankSubtitle); err == nil {
		t.Fatal("expected blank login subtitle to be rejected")
	}

	blankFormTitle := valid
	blankFormTitle.LoginFormTitle = " "
	if err := ValidateSite(blankFormTitle); err == nil {
		t.Fatal("expected blank login form title to be rejected")
	}

	blankFormSubtitle := valid
	blankFormSubtitle.LoginFormSubtitle = " "
	if err := ValidateSite(blankFormSubtitle); err == nil {
		t.Fatal("expected blank login form subtitle to be rejected")
	}

	unsafeBackground := valid
	unsafeBackground.LoginBackgroundURL = "javascript:alert(1)"
	if err := ValidateSite(unsafeBackground); err == nil {
		t.Fatal("expected unsafe background URL to be rejected")
	}

	invalidSource := valid
	invalidSource.LoginBackgroundSource = "unknown"
	if err := ValidateSite(invalidSource); err == nil {
		t.Fatal("expected invalid background source to be rejected")
	}

	invalidInterval := valid
	invalidInterval.LoginBackgroundInterval = 4
	if err := ValidateSite(invalidInterval); err == nil {
		t.Fatal("expected short carousel interval to be rejected")
	}
}
