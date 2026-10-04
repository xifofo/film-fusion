package service

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/model"
)

// Deliberately unsorted info keys: the hash must use the original byte span.
const torrentMagnetTestInfo = "d4:name8:demo.mkv6:lengthi5e12:piece lengthi16384e6:pieces20:abcdefghijklmnopqrste"

func encodeTorrentFixture(value any) string {
	switch v := value.(type) {
	case string:
		return fmt.Sprintf("%d:%s", len(v), v)
	case int:
		return fmt.Sprintf("i%de", v)
	case []any:
		out := "l"
		for _, item := range v {
			out += encodeTorrentFixture(item)
		}
		return out + "e"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := "d"
		for _, key := range keys {
			out += encodeTorrentFixture(key) + encodeTorrentFixture(v[key])
		}
		return out + "e"
	default:
		panic("invalid fixture")
	}
}

func TestTorrentMagnetHashesOriginalInfoAndKeepsMetadata(t *testing.T) {
	seed := "d8:announce" + encodeTorrentFixture("https://tracker.example/announce?passkey=a&b=c") + "4:info" + torrentMagnetTestInfo + "e"
	output, err := parseTorrentMagnet([]byte(seed), true)
	if err != nil {
		t.Fatal(err)
	}
	if output["info_hash"] != "64dc107e3a558c1353ab10bb55f463c787b43555" || output["torrent_name"] != "demo.mkv" || output["file_size"] != int64(5) || output["file_count"] != 1 || output["private"] != false {
		t.Fatalf("incorrect metadata: %#v", output)
	}
	magnet, _ := url.Parse(output["magnet"].(string))
	if magnet.Query().Get("xt") != "urn:btih:"+output["info_hash"].(string) || magnet.Query().Get("tr") != "https://tracker.example/announce?passkey=a&b=c" {
		t.Fatalf("incorrect magnet: %s", magnet)
	}
	other, err := parseTorrentMagnet([]byte("d7:comment7:changed4:info"+torrentMagnetTestInfo+"e"), false)
	if err != nil || other["info_hash"] != output["info_hash"] || strings.Contains(other["magnet"].(string), "tr=") {
		t.Fatalf("outer metadata changed hash: %#v, %v", other, err)
	}
}

func TestTorrentMagnetMultiFilePrivateAndHybrid(t *testing.T) {
	info := map[string]any{
		"name": "fallback", "name.utf-8": "测试 剧集", "private": 1, "meta version": 2,
		"piece length": 16384, "pieces": strings.Repeat("\x00", 20),
		"files": []any{
			map[string]any{"length": 2, "path": []any{"one.mkv"}},
			map[string]any{"length": 3, "path.utf-8": []any{"字幕", "二.srt"}},
		},
	}
	tracker := "udp://tracker.example:80/announce"
	seed := encodeTorrentFixture(map[string]any{"info": info, "announce": tracker, "announce-list": []any{[]any{tracker, "https://two.example/announce"}}})
	output, err := parseTorrentMagnet([]byte(seed), false)
	if err != nil {
		t.Fatal(err)
	}
	files := output["files"].([]map[string]any)
	if output["private"] != true || output["torrent_version"] != "hybrid" || len(files) != 2 || files[1]["path"] != "测试 剧集/字幕/二.srt" || output["file_size"] != int64(5) || len(output["warnings"].([]string)) != 1 || len(output["trackers"].([]string)) != 2 {
		t.Fatalf("incorrect multi-file output: %#v", output)
	}
	magnet, _ := url.Parse(output["magnet"].(string))
	if magnet.Query().Get("dn") != "测试 剧集" || magnet.Query().Has("tr") {
		t.Fatalf("incorrect magnet: %s", magnet)
	}
}

func TestTorrentMagnetRejectsMalformedAndUnsupportedData(t *testing.T) {
	valid := "d4:info" + torrentMagnetTestInfo + "e"
	cases := []string{
		"", "<html>login</html>", valid + "garbage", valid[:len(valid)-1], "d4:infoi1ee",
		"d4:info" + torrentMagnetTestInfo + "4:info" + torrentMagnetTestInfo + "e",
		strings.Replace(valid, "i5e", "i-1e", 1), strings.Replace(valid, "i5e", "i05e", 1),
		strings.Replace(valid, "i5e", "i999999e", 1), strings.Replace(valid, "i16384e", "i0e", 1),
		"d4:infod12:meta versioni2eee", "d4:infod12:meta versioni3eee",
		strings.Repeat("l", 66) + strings.Repeat("e", 66), strings.Repeat("a", maxAutomationTorrentBytes+1),
		"d4:infod4:name1:x5:filesld6:lengthi9223372036854775807e4:pathl1:aeed6:lengthi1e4:pathl1:beeeee",
	}
	for i, seed := range cases {
		if output, err := parseTorrentMagnet([]byte(seed), true); err == nil || output != nil {
			t.Errorf("case %d accepted invalid torrent: %#v", i, output)
		}
	}
}

func TestTorrentMagnetPassthroughAndUnknownMetadata(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	bytes, _ := hex.DecodeString(hash)
	for _, raw := range []string{strings.ToUpper(hash), base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes)} {
		out, err := executeAutomationTorrentToMagnet(context.Background(), AutomationNode{Config: map[string]any{"url": "magnet:?xt=urn:btih:" + raw + "&dn=Example&tr=https%3A%2F%2Ftracker.example", "include_trackers": false}}, nil)
		if err != nil || out["info_hash"] != hash || out["metadata_available"] != false || out["private"] != nil || out["file_count"] != nil || strings.Contains(out["magnet"].(string), "tr=") {
			t.Fatalf("incorrect passthrough: %#v, %v", out, err)
		}
	}
	for _, invalid := range []string{"magnet:?xt=urn:btih:abc", "magnet:?xt=urn:btmh:1220abc", "magnet:?xt=urn:btih:" + hash + "&xt=urn:btih:" + strings.Repeat("a", 40)} {
		if _, err := passthroughTorrentMagnet(invalid, true); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestAutomationTorrentTo115LinkWithOnlyLocalMocks(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/seed", http.StatusFound)
			return
		}
		if r.Header.Get("X-Test") != "value" || r.Method != "GET" {
			t.Error("seed headers/method were not preserved")
		}
		fmt.Fprint(w, "d4:info"+torrentMagnetTestInfo+"e")
	}))
	defer server.Close()
	submits := 0
	s, storage := newAutomation115APITest(t, func(r *http.Request) string {
		submits++
		if r.URL.Path != "/open/offline/add_task_urls" || !strings.Contains(r.FormValue("urls"), "urn%3Abtih%3A") {
			t.Fatalf("incorrect 115 link submission: %s, %v", r.URL.Path, r.Form)
		}
		return `{"state":true,"code":0,"message":"","data":[{"state":true,"code":0,"info_hash":"accepted-hash","url":"magnet"}]}`
	})
	node := AutomationNode{ID: "convert", Type: AutomationNodeTorrentToMagnet, Config: map[string]any{"url": "$item.download_url", "allow_private_network": true, "headers": map[string]any{"X-Test": "$item.header"}}}
	output, err := s.executeAutomationNode(context.Background(), model.AutomationRun{}, model.AutomationNodeRun{}, node, AutomationDefinition{}, map[string]any{"item": map[string]any{"download_url": server.URL + "/redirect", "header": "value"}})
	if err != nil || output["selected_port"] != "success" || reads.Load() != 2 {
		t.Fatalf("conversion failed: %#v, %v", output, err)
	}
	add := AutomationNode{Type: AutomationNodeOffline115OpenAPI, Config: map[string]any{"cloud_storage_id": storage.ID, "url": "$nodes.convert.output.magnet"}}
	result, err := s.executeAutomationOffline115OpenAPI(context.Background(), add, automationTestRunContext("convert", output))
	if err != nil || result["info_hash"] != "accepted-hash" || submits != 1 {
		t.Fatalf("submission failed: %#v, %v", result, err)
	}
}

func TestAutomationTorrentFetchFailures(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		switch r.URL.Path {
		case "/large":
			w.Header().Set("Content-Length", fmt.Sprint(maxAutomationTorrentBytes+1))
		case "/redirect":
			http.Redirect(w, r, "https://other.example/seed", http.StatusFound)
		case "/login":
			fmt.Fprint(w, "<html>login</html>")
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()
	for _, path := range []string{"/large", "/redirect", "/login", "/forbidden"} {
		_, err := executeAutomationTorrentToMagnet(context.Background(), AutomationNode{Config: map[string]any{"url": server.URL + path + "?secret=hidden", "allow_private_network": true}}, nil)
		if err == nil || strings.Contains(err.Error(), "secret=hidden") {
			t.Fatalf("unexpected error on %s: %v", path, err)
		}
	}
	count := reads.Load()
	_, err := executeAutomationTorrentToMagnet(context.Background(), AutomationNode{Config: map[string]any{"url": server.URL}}, nil)
	if err == nil || reads.Load() != count {
		t.Fatal("private network was not blocked")
	}
}

func FuzzTorrentMagnet(f *testing.F) {
	f.Add([]byte("d4:info" + torrentMagnetTestInfo + "e"))
	f.Add([]byte("<html>not a torrent</html>"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = parseTorrentMagnet(data, true) })
}
