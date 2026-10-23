package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func upgradeMatchCondition(field string, values ...string) MediaUpgradeCondition {
	return MediaUpgradeCondition{Op: "match", Field: field, Values: values}
}

func TestUpgradeConditionsThreeValuedLogic(t *testing.T) {
	audio := upgradeMatchCondition("audio_codec", "AAC", "DDP")
	resolution := upgradeMatchCondition("resolution", "1080p")
	for _, tc := range []struct {
		name      string
		condition MediaUpgradeCondition
		facts     MediaUpgradeFacts
		want      upgradeMatchResult
	}{
		{"unknown audio", audio, MediaUpgradeFacts{}, upgradeMatchUnknown},
		{"not unknown remains unknown", MediaUpgradeCondition{Op: "not", Children: []MediaUpgradeCondition{audio}}, MediaUpgradeFacts{}, upgradeMatchUnknown},
		{"known audio mismatch", audio, MediaUpgradeFacts{AudioCodec: "DTS"}, upgradeMatchFalse},
		{"choices are OR case insensitive", audio, MediaUpgradeFacts{AudioCodec: "ddp"}, upgradeMatchTrue},
		{"all false dominates unknown", MediaUpgradeCondition{Op: "all", Children: []MediaUpgradeCondition{audio, resolution}}, MediaUpgradeFacts{Resolution: "2160p"}, upgradeMatchFalse},
		{"all true and unknown", MediaUpgradeCondition{Op: "all", Children: []MediaUpgradeCondition{audio, resolution}}, MediaUpgradeFacts{Resolution: "1080p"}, upgradeMatchUnknown},
		{"any true dominates unknown", MediaUpgradeCondition{Op: "any", Children: []MediaUpgradeCondition{audio, resolution}}, MediaUpgradeFacts{Resolution: "1080p"}, upgradeMatchTrue},
		{"any false and unknown", MediaUpgradeCondition{Op: "any", Children: []MediaUpgradeCondition{audio, resolution}}, MediaUpgradeFacts{Resolution: "2160p"}, upgradeMatchUnknown},
		{"not known false", MediaUpgradeCondition{Op: "not", Children: []MediaUpgradeCondition{audio}}, MediaUpgradeFacts{AudioCodec: "DTS"}, upgradeMatchTrue},
		{"contains ignores case", MediaUpgradeCondition{Op: "match", Field: "name", Match: "contains", Values: []string{"REPACK"}}, MediaUpgradeFacts{Original: "Show.repack.mkv"}, upgradeMatchTrue},
		{"regex preserves case", MediaUpgradeCondition{Op: "match", Field: "name", Match: "regex", Values: []string{"REPACK"}}, MediaUpgradeFacts{Original: "Show.repack.mkv"}, upgradeMatchFalse},
		{"regex supports explicit ignore case", MediaUpgradeCondition{Op: "match", Field: "name", Match: "regex", Values: []string{`(?i)\.repack\.`}}, MediaUpgradeFacts{Original: "Show.repack.mkv"}, upgradeMatchTrue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateUpgradeCondition(&tc.condition); err != nil {
				t.Fatal(err)
			}
			if got := evaluateUpgradeCondition(&tc.condition, tc.facts); got != tc.want {
				t.Fatalf("result=%v, want=%v", got, tc.want)
			}
		})
	}
}

func TestUpgradeConditionsCombinedRulesAndExclusions(t *testing.T) {
	condition := MediaUpgradeCondition{Op: "all", Children: []MediaUpgradeCondition{
		{Op: "any", Children: []MediaUpgradeCondition{upgradeMatchCondition("subtitle", "zh"), upgradeMatchCondition("audio_language", "zh")}},
		{Op: "not", Children: []MediaUpgradeCondition{upgradeMatchCondition("audio_codec", "DTS")}},
	}}
	policy := MediaUpgradePolicy{Enabled: true, Rules: []MediaUpgradeRule{
		{Name: "中文兼容", Resolutions: []string{"1080p"}, AudioChannels: []string{"2.0", "5.1"}, Condition: &condition, IncludeRegex: `(?i)\.web-dl\.`, Exclude: "sample", ExcludeRegex: `(?i)\btrailer\b`},
		{Name: "其他", Resolutions: []string{"1080p", "2160p"}},
	}}
	if err := ValidateMediaUpgradePolicy(policy); err != nil {
		t.Fatal(err)
	}
	facts := MediaUpgradeFacts{Original: "Show.WEB-DL.mkv", Resolution: "1080p", AudioCodec: "AAC", AudioChannels: "2.0", Subtitles: []string{"zh"}}
	if got := RankMediaUpgrade(policy, facts); got.Tier != 1 {
		t.Fatalf("rank=%+v", got)
	}
	for _, change := range []func(*MediaUpgradeFacts){
		func(f *MediaUpgradeFacts) { f.Resolution = "2160p" },
		func(f *MediaUpgradeFacts) { f.Original = "Show.WEB-DL.SAMPLE.mkv" },
		func(f *MediaUpgradeFacts) { f.Original = "Show.WEB-DL.Trailer.mkv" },
		func(f *MediaUpgradeFacts) { f.AudioCodec = "DTS" },
	} {
		changed := facts
		change(&changed)
		if got := RankMediaUpgrade(policy, changed); got.Tier != 2 {
			t.Fatalf("expected fallback: %+v", got)
		}
	}
	exclude := MediaUpgradeCondition{Op: "all", Children: []MediaUpgradeCondition{
		upgradeMatchCondition("resolution", "1080p"),
		{Op: "not", Children: []MediaUpgradeCondition{upgradeMatchCondition("audio_language", "zh")}},
	}}
	policy.ExcludeCondition = &exclude
	facts.AudioLanguages = []string{"en"}
	if got := RankMediaUpgrade(policy, facts); !got.Rejected {
		t.Fatalf("not excluded: %+v", got)
	}
	facts.AudioLanguages = nil
	if got := RankMediaUpgrade(policy, facts); got.Rejected || got.Tier != 0 || !strings.Contains(got.Label, "未知") {
		t.Fatalf("unknown exclusion: %+v", got)
	}
}

func TestUpgradeUnknownConditionsCannotAuthorizeReplacement(t *testing.T) {
	notDTS := MediaUpgradeCondition{Op: "not", Children: []MediaUpgradeCondition{upgradeMatchCondition("audio_codec", "DTS")}}
	policy := MediaUpgradePolicy{Enabled: true, Rules: []MediaUpgradeRule{
		{Name: "非 DTS", Condition: &notDTS},
		{Name: "基本", Resolutions: []string{"1080p"}},
	}}
	old := MediaUpgradeFacts{Resolution: "1080p", AudioCodec: "DTS"}
	unknown := MediaUpgradeFacts{Resolution: "1080p"}
	if action, reason := CompareMediaUpgrade(policy, unknown, old); action != "review" {
		t.Fatalf("%s: %s", action, reason)
	}
	if action, reason := CompareMediaUpgrade(policy, old, unknown); action != "review" {
		t.Fatalf("%s: %s", action, reason)
	}
	policy.Rules = []MediaUpgradeRule{{Name: "基本", Resolutions: []string{"1080p"}}}
	policy.Bonuses = []MediaUpgradeRule{{Name: "非 DTS", Condition: &notDTS, Score: 10}}
	policy.SameTier = true
	if action, _ := CompareMediaUpgrade(policy, unknown, old); action != "review" {
		t.Fatalf("unknown bonus allowed action %s", action)
	}
	policy.SameTier = false
	if action, _ := CompareMediaUpgrade(policy, unknown, old); action != "keep" {
		t.Fatalf("unused bonus affected action %s", action)
	}
}

func TestUpgradeLanguageDeclarationsAndHistoricalFacts(t *testing.T) {
	facts := ParseMediaUpgradeFacts("Example.1080p.WEB-DL.AAC2.0.国语.粤语.CHS&ENG.mkv", 10)
	if facts.AudioCodec != "AAC" || facts.AudioChannels != "2.0" || !reflect.DeepEqual(facts.AudioLanguages, []string{"zh", "yue"}) || !upgradeContains(facts.Subtitles, "zh-Hans") || !upgradeContains(facts.Subtitles, "zh") || !upgradeContains(facts.Subtitles, "en") {
		t.Fatalf("facts=%+v", facts)
	}
	for _, name := range []string{"Example.1080p.mkv", "English.Patient.ENG.JPN.1080p.mkv", "Example.英语字幕.日语字幕.mkv"} {
		parsed := ParseMediaUpgradeFacts(name, 0)
		if len(parsed.AudioLanguages) != 0 {
			t.Fatalf("inferred audio language from %q: %+v", name, parsed)
		}
	}
	var historical MediaUpgradeFacts
	if err := json.Unmarshal([]byte(`{"original":"Original.AAC2.0.国语.简繁中字.mkv","resolution":"2160p","codec":"H.265","group":"Saved","size":123}`), &historical); err != nil {
		t.Fatal(err)
	}
	enriched := EnrichMediaUpgradeFacts(historical)
	if enriched.Resolution != "2160p" || enriched.Codec != "H.265" || enriched.Group != "Saved" || enriched.Size != 123 || enriched.AudioCodec != "AAC" || !upgradeContains(enriched.Subtitles, "zh-Hant") {
		t.Fatalf("historical evidence changed or not enriched: %+v", enriched)
	}
	policy := MediaUpgradePolicy{Rules: []MediaUpgradeRule{{Name: "audio", AudioCodecs: []string{"AAC"}, Subtitles: []string{"zh"}}}}
	if rank := RankMediaUpgrade(policy, historical); rank.Tier != 1 {
		t.Fatalf("rank did not enrich history: %+v", rank)
	}
	historical.AudioCodec, historical.AudioChannels = "DTS", "7.1"
	historical.AudioLanguages, historical.Subtitles = []string{"ja"}, []string{"en"}
	if got := EnrichMediaUpgradeFacts(historical); !reflect.DeepEqual(got, historical) {
		t.Fatalf("explicit new evidence overwritten: %+v", got)
	}
}

func TestUpgradeLanguageLabelsDoNotReadTitlesAsMetadata(t *testing.T) {
	for _, name := range []string{
		"疯狂英语.1999.1080p.WEB-DL.mkv",
		"中英街一号.2018.1080p.WEB-DL.mkv",
		"Johnny.English.2011.1080p.WEB-DL.mkv",
		"English.Vinglish.2012.1080p.WEB-DL.mkv",
		"Mandarin.2024.1080p.WEB-DL.mkv",
		"[疯狂英语].1999.1080p.WEB-DL.mkv",
		"[中英街一号].2018.1080p.WEB-DL.mkv",
		"Show.2024.1080p.[无英语字幕].mkv",
	} {
		t.Run(name, func(t *testing.T) {
			facts := ParseMediaUpgradeFacts(name, 10)
			if len(facts.AudioLanguages) != 0 || len(facts.Subtitles) != 0 {
				t.Fatalf("title interpreted as language evidence: %+v", facts)
			}
			for _, field := range []string{"audio_language", "subtitle"} {
				condition := MediaUpgradeCondition{Op: "not", Children: []MediaUpgradeCondition{upgradeMatchCondition(field, "en")}}
				if got := evaluateUpgradeCondition(&condition, facts); got != upgradeMatchUnknown {
					t.Fatalf("missing %s must remain unknown under NOT, got %v", field, got)
				}
			}
		})
	}
	for _, tc := range []struct {
		name      string
		audio     []string
		subtitles []string
	}{
		{"Example.2024.1080p.国语中字.mkv", []string{"zh"}, []string{"zh"}},
		{"Example.2024.1080p.国语配音.中英字幕.mkv", []string{"zh"}, []string{"zh", "en"}},
		{"Example.[国语中字].mkv", []string{"zh"}, []string{"zh"}},
		{"Example.【国语配音】【中字】.mkv", []string{"zh"}, []string{"zh"}},
		{"[中英字幕]Example.mkv", nil, []string{"zh", "en"}},
		{"Example（简繁中字）.mkv", nil, []string{"zh-Hans", "zh-Hant", "zh"}},
		{"Example.2024.内嵌简中.英语字幕.mkv", nil, []string{"zh-Hans", "zh", "en"}},
		{"Johnny.English.2011.English.Audio.ENG.Sub.mkv", []string{"en"}, []string{"en"}},
		{"Example.S01E01.Mandarin.CHS.mkv", []string{"zh"}, []string{"zh-Hans", "zh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := ParseMediaUpgradeFacts(tc.name, 10)
			if len(facts.AudioLanguages) != len(tc.audio) || len(facts.Subtitles) != len(tc.subtitles) {
				t.Fatalf("unexpected declarations: %+v", facts)
			}
			for _, value := range tc.audio {
				if !upgradeContains(facts.AudioLanguages, value) {
					t.Fatalf("missing audio %s: %+v", value, facts)
				}
			}
			for _, value := range tc.subtitles {
				if !upgradeContains(facts.Subtitles, value) {
					t.Fatalf("missing subtitle %s: %+v", value, facts)
				}
			}
		})
	}
}

func TestUpgradeConditionValidation(t *testing.T) {
	valid := upgradeMatchCondition("audio_codec", "AAC")
	deep := valid
	for i := 0; i < maxUpgradeConditionDepth; i++ {
		deep = MediaUpgradeCondition{Op: "not", Children: []MediaUpgradeCondition{deep}}
	}
	wide := MediaUpgradeCondition{Op: "any"}
	for i := 0; i < maxUpgradeConditionNodes; i++ {
		wide.Children = append(wide.Children, valid)
	}
	for _, invalid := range []MediaUpgradeCondition{
		{Op: "xor"}, {Op: "all"}, {Op: "not", Children: []MediaUpgradeCondition{valid, valid}},
		{Op: "all", Field: "name", Children: []MediaUpgradeCondition{valid}},
		{Op: "match", Field: "unknown", Values: []string{"x"}},
		{Op: "match", Field: "name"},
		{Op: "match", Field: "name", Values: []string{" "}},
		{Op: "match", Field: "name", Values: []string{strings.Repeat("x", 121)}},
		{Op: "match", Field: "name", Values: []string{"x"}, Match: "invalid"},
		{Op: "match", Field: "name", Values: []string{"("}, Match: "regex"},
		{Op: "match", Field: "name", Values: []string{"x"}, Children: []MediaUpgradeCondition{valid}}, deep, wide,
	} {
		if err := validateUpgradeCondition(&invalid); err == nil {
			t.Fatalf("accepted invalid: %+v", invalid)
		}
	}
	for _, rule := range []MediaUpgradeRule{{Name: "bad", IncludeRegex: "("}, {Name: "bad", ExcludeRegex: "("}, {Name: "bad", AudioCodecs: []string{""}}} {
		if err := ValidateMediaUpgradePolicy(MediaUpgradePolicy{Rules: []MediaUpgradeRule{rule}}); err == nil {
			t.Fatalf("accepted invalid rule: %+v", rule)
		}
	}
}

func TestUpgradePolicyReferencesAndLegacyCompatibility(t *testing.T) {
	for _, raw := range []string{`{"enabled":true,"group_id":42}`, `{"enabled":false,"group_id":42}`, `{"enabled":true,"group_id":42,"group_revision":3,"rules":[{"name":"HD","resolutions":["1080p"]}]}`} {
		if _, err := ParseMediaUpgradePolicy(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"enabled":true,"group_id":42,"rules":[{"name":"HD","resolutions":["1080p"]}]}`, `{"group_revision":1}`, `{"group_id":42,"group_revision":1}`, `{"group_id":42,"same_tier":true}`, `{"group_id":42,"exclude_condition":{"op":"match","field":"name","values":["x"]}}`} {
		if _, err := ParseMediaUpgradePolicy(raw); err == nil {
			t.Fatalf("accepted invalid ref %s", raw)
		}
	}
	ref := MediaUpgradePolicy{Enabled: true, GroupID: 42}
	if rank := RankMediaUpgrade(ref, MediaUpgradeFacts{Resolution: "1080p"}); rank.Tier != 0 {
		t.Fatalf("ranked unresolved ref %+v", rank)
	}
	legacy := `{"enabled":true,"name":"legacy","rules":[{"name":"4K","resolutions":["2160p"]},{"name":"HD","resolutions":["1080p"]}],"bonuses":[{"name":"AAC","include":"AAC","score":1000}],"exclude":["CAM"],"same_tier":true,"min_score_gain":10}`
	policy, err := ParseMediaUpgradePolicy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if action, _ := CompareMediaUpgrade(policy, ParseMediaUpgradeFacts("Show.1080p.AAC.mkv", 10), ParseMediaUpgradeFacts("Show.2160p.mkv", 10)); action != "keep" {
		t.Fatalf("bonus crossed tier: %s", action)
	}
	if action, _ := CompareMediaUpgrade(policy, ParseMediaUpgradeFacts("Show.1080p.AAC.mkv", 10), ParseMediaUpgradeFacts("Show.1080p.mkv", 10)); action != "replace" {
		t.Fatalf("legacy bonus changed: %s", action)
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "condition") || strings.Contains(string(encoded), "group_id") || strings.Contains(string(encoded), "audio_codecs") {
		t.Fatalf("legacy JSON grew optional fields: %s", encoded)
	}
}
