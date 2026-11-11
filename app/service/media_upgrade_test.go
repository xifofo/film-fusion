package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaUpgradePolicies(t *testing.T) {
	quality := MediaUpgradePresets()["quality"]
	quality.Enabled = true
	// 预设默认开启同档加分；先关闭以验证同档保留旧版的基础语义。
	quality.SameTier = false
	for _, tc := range []struct{ name, incoming, existing, action string }{
		{"higher resolution", "Example.2160p.WEB-DL.H265.mkv", "Example.1080p.REMUX.H264.mkv", "replace"},
		{"better source", "Example.1080p.REMUX.H264.mkv", "Example.1080p.WEB-DL.H264.mkv", "replace"},
		{"no downgrade", "Example.1080p.REMUX.H264.mkv", "Example.2160p.WEB-DL.H265.mkv", "keep"},
		{"equal quality", "Example.1080p.WEB-DL.PROPER.H264.mkv", "Example.1080p.WEB-DL.H264.mkv", "keep"},
		{"unknown old", "Example.2160p.REMUX.mkv", "Example.mkv", "review"},
		{"unknown new", "Example.mkv", "Example.1080p.WEB-DL.mkv", "review"},
		{"excluded", "Example.2160p.CAM.H265.mkv", "Example.1080p.WEB-DL.mkv", "keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := CompareMediaUpgrade(quality, ParseMediaUpgradeFacts(tc.incoming, 1000), ParseMediaUpgradeFacts(tc.existing, 1000))
			if got != tc.action {
				t.Fatalf("action %s, want %s (%s); facts=%+v", got, tc.action, reason, ParseMediaUpgradeFacts(tc.incoming, 1000))
			}
		})
	}
	quality.SameTier = true
	newFacts := ParseMediaUpgradeFacts("Example.1080p.WEB-DL.PROPER.REPACK.H264.mkv", 1000)
	oldFacts := ParseMediaUpgradeFacts("Example.1080p.WEB-DL.H264.mkv", 1000)
	if rank := RankMediaUpgrade(quality, newFacts); rank.Score != 10 {
		t.Fatalf("bonus counted more than once: %+v", rank)
	}
	if action, _ := CompareMediaUpgrade(quality, newFacts, oldFacts); action != "replace" {
		t.Fatal("same-tier upgrade not allowed")
	}
	quality.MinScoreGain = 11
	if action, _ := CompareMediaUpgrade(quality, newFacts, oldFacts); action != "keep" {
		t.Fatal("minimum gain ignored")
	}
	quality.MinScoreGain = 0
	if action, _ := CompareMediaUpgrade(quality, oldFacts, oldFacts); action != "keep" {
		t.Fatal("zero gain caused replacement loop")
	}
	quality.Bonuses = []MediaUpgradeRule{{Name: "Huge bonus", Resolutions: []string{"1080p"}, Score: 1000}}
	if action, _ := CompareMediaUpgrade(quality, newFacts, ParseMediaUpgradeFacts("Example.2160p.WEB-DL.mkv", 1000)); action != "keep" {
		t.Fatal("bonus crossed tier boundary")
	}
	space := MediaUpgradePresets()["space"]
	if !RankMediaUpgrade(space, ParseMediaUpgradeFacts("Example.1080p.H265.mkv", 30*1024*1024*1024)).Rejected {
		t.Fatal("size limit ignored")
	}
	if rank := RankMediaUpgrade(space, ParseMediaUpgradeFacts("Example.1080p.H265.mkv", 0)); rank.Tier != 0 {
		t.Fatal("unknown size accepted")
	}
	compatible := MediaUpgradePresets()["compatible"]
	if rank := RankMediaUpgrade(compatible, ParseMediaUpgradeFacts("Example.1080p.WEB-DL.DV.H264.mkv", 1000)); !rank.Rejected {
		t.Fatalf("Dolby Vision exclusion ignored: %+v", rank)
	}
	for _, raw := range []string{`{"enabled":true}`, `{"enabled":true,"typo":1}`, `{} {}`, `{"rules":[{"name":"empty"}]}`} {
		if _, err := ParseMediaUpgradePolicy(raw); err == nil {
			t.Fatalf("invalid policy accepted: %s", raw)
		}
	}
}

func TestMediaUpgradePresetTemplates(t *testing.T) {
	presets := MediaUpgradePresets()
	for key, policy := range presets {
		policy.Enabled = true
		if err := ValidateMediaUpgradePolicy(policy); err != nil {
			t.Fatalf("preset %s invalid: %v", key, err)
		}
		if len(policy.Bonuses) > 0 && !policy.SameTier {
			t.Fatalf("preset %s has bonuses that can never apply", key)
		}
	}

	quality := presets["quality"]
	proper := ParseMediaUpgradeFacts("Example.1080p.WEB-DL.PROPER.H264.mkv", 1000)
	plain := ParseMediaUpgradeFacts("Example.1080p.WEB-DL.H264.mkv", 1000)
	if action, _ := CompareMediaUpgrade(quality, proper, plain); action != "replace" {
		t.Fatal("preset fix bonus did not apply")
	}

	dolby := presets["dolby_vision"]
	for _, tc := range []struct{ incoming, existing, action string }{
		{"Example.2160p.WEB-DL.DV.H265.mkv", "Example.2160p.BluRay.HDR10.H265.mkv", "replace"},
		{"Example.2160p.WEB-DL.HDR10.H265.mkv", "Example.2160p.WEB-DL.H265.mkv", "replace"},
		{"Example.1080p.WEB-DL.DV.H265.mkv", "Example.2160p.WEB-DL.H265.mkv", "keep"},
	} {
		if action, reason := CompareMediaUpgrade(dolby, ParseMediaUpgradeFacts(tc.incoming, 1000), ParseMediaUpgradeFacts(tc.existing, 1000)); action != tc.action {
			t.Fatalf("%s vs %s: %s (%s)", tc.incoming, tc.existing, action, reason)
		}
	}

	clean := presets["clean"]
	if !RankMediaUpgrade(clean, ParseMediaUpgradeFacts("Example.1080p.HDTV.H264.mkv", 1000)).Rejected {
		t.Fatal("low quality source accepted")
	}
}

func TestMediaUpgradeUnmatchedAction(t *testing.T) {
	policy := MediaUpgradePolicy{Enabled: true, Rules: []MediaUpgradeRule{{Name: "4K", Resolutions: []string{"2160p"}}}}
	for _, value := range []string{"", MediaUpgradeUnmatchedReview, MediaUpgradeUnmatchedAdd} {
		policy.UnmatchedAction = value
		if err := ValidateMediaUpgradePolicy(policy); err != nil {
			t.Fatalf("unmatched %q rejected: %v", value, err)
		}
	}
	policy.UnmatchedAction = "replace"
	if err := ValidateMediaUpgradePolicy(policy); err == nil {
		t.Fatal("unknown unmatched action accepted")
	}
	if err := ValidateMediaUpgradePolicy(MediaUpgradePolicy{Enabled: true, GroupID: 3, UnmatchedAction: MediaUpgradeUnmatchedAdd}); err == nil {
		t.Fatal("group reference mixed inline unmatched action")
	}

	// 允许直接入库只影响无旧版本的新增；有旧版本时仍不能凭未知档位替换。
	policy.UnmatchedAction = MediaUpgradeUnmatchedAdd
	if action, _ := CompareMediaUpgrade(policy, ParseMediaUpgradeFacts("Example.1080p.mkv", 1000), ParseMediaUpgradeFacts("Example.720p.mkv", 1000)); action != "review" {
		t.Fatal("unmatched incoming replaced an old version")
	}
	if MediaUpgradeTierOrder(0) <= MediaUpgradeTierOrder(50) {
		t.Fatal("unmatched tier sorted before real tiers")
	}
}

func TestUpgradeLocalFilesAreReversibleAndRejectUnrelatedChanges(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "film", "title.strm")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := ReadUpgradeFile(root, target)
	if err != nil {
		t.Fatal(err)
	}
	change := UpgradeFileChange{Before: before, Write: true, Data: []byte("new")}
	for i := 0; i < 2; i++ {
		if err := ApplyUpgradeFile(change, false); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := ApplyUpgradeFile(change, true); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(target)
	if string(data) != "old" {
		t.Fatalf("restore got %q", data)
	}
	if err := os.WriteFile(target, []byte("user edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpgradeFile(change, false); err == nil {
		t.Fatal("unrelated edit overwritten")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUpgradeFile(root, filepath.Join(root, "escape", "file.strm")); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err := ReadUpgradeFile(root, filepath.Join(outside, "file.strm")); err == nil {
		t.Fatal("outside path accepted")
	}
	if _, err := ReadUpgradeFile("relative", target); err == nil {
		t.Fatal("relative root accepted")
	}
	if !IsUpgradeRecoveryPath("/FilmFusion-Recovery/a") || IsUpgradeRecoveryPath("/FilmFusion-Recovery2/a") {
		t.Fatal("recovery path boundary")
	}
}

func TestUpgradeCutAndPartFacts(t *testing.T) {
	facts := ParseMediaUpgradeFacts("Example.2024.Directors.Cut.Part2.2160p.REMUX.mkv", 1)
	if facts.Cut != "directors-cut" || facts.Part != 2 {
		t.Fatalf("edition facts lost: %+v", facts)
	}
	facts = ParseMediaUpgradeFacts("Example.2024.Extended.1080p.BluRay.mkv", 1)
	if !strings.EqualFold(facts.Cut, "extended") {
		t.Fatalf("extended cut lost: %+v", facts)
	}
}
