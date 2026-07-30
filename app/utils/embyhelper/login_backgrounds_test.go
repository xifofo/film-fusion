package embyhelper

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"film-fusion/app/config"
)

func TestListLoginBackgroundItemsPopular(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/admin/Items" {
			t.Fatalf("path=%q want /Users/admin/Items", r.URL.Path)
		}
		if got := r.URL.Query().Get("SortBy"); got != "PlayCount,CommunityRating,SortName" {
			t.Fatalf("SortBy=%q", got)
		}
		if got := r.URL.Query().Get("ImageTypes"); got != "Backdrop" {
			t.Fatalf("ImageTypes=%q", got)
		}
		if got := r.URL.Query().Get("api_key"); got != "emby-key" {
			t.Fatalf("api_key=%q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Items":[
			{"Id":"without-backdrop","Name":"No Image"},
			{"Id":"movie-1","Name":"Movie","BackdropImageTags":["tag-1"]},
			{"Id":"series-2","Name":"Series","BackdropImageTags":["tag-2"]}
		]}`)
	}))
	defer server.Close()

	client := New(&config.Config{Emby: config.EmbyConfig{
		URL:         server.URL,
		APIKey:      "emby-key",
		AdminUserID: "admin",
	}})
	items, err := client.ListLoginBackgroundItems("popular", 2)
	if err != nil {
		t.Fatalf("list login backgrounds: %v", err)
	}
	if len(items) != 2 || items[0].ID != "movie-1" || items[1].ID != "series-2" {
		t.Fatalf("unexpected items: %+v", items)
	}
}
