// 网盘发现协议通过本地 HTTP 模拟验证分页、身份一致性和密钥不跟随重定向。
package plutofish

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDiscoverAccountsPaginatesWithoutEvents(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/events/accounts" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-secret" || r.URL.Query().Get("limit") != "100" || strings.Contains(r.URL.String(), "fixture-secret") {
			t.Error("invalid discovery request or leaked key")
		}
		if calls == 1 {
			fmt.Fprint(w, `{"ownerUID":"owner","items":[{"id":"a","name":"主网盘","provider":"115","enabled":true}],"nextAfter":"a","hasMore":true}`)
		} else {
			if r.URL.Query().Get("after") != "a" {
				t.Error("lost account pagination cursor")
			}
			fmt.Fprint(w, `{"ownerUID":"owner","items":[{"id":"b","name":"备用网盘","provider":"115","enabled":false}],"nextAfter":"b","hasMore":false}`)
		}
	}))
	defer server.Close()
	result, err := DiscoverAccounts(context.Background(), server.URL, "fixture-secret")
	if err != nil || calls != 2 || result.OwnerUID != "owner" || len(result.Items) != 2 || result.Items[1].Enabled {
		t.Fatalf("unexpected discovery result: %+v, %v", result, err)
	}
}

func TestDiscoverAccountsRejectsPartialAndInvalidResults(t *testing.T) {
	for _, body := range []string{
		`{"ownerUID":"different-owner","items":[],"hasMore":false}`,
		`{"ownerUID":"owner","items":[{"id":"a","name":"重复","provider":"115","enabled":true}],"hasMore":false}`,
		`{"ownerUID":"owner","items":[],"nextAfter":"a","hasMore":true}`,
		`{"items":[],"hasMore":false}`,
		`not-json`,
	} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					fmt.Fprint(w, `{"ownerUID":"owner","items":[{"id":"a","name":"主网盘","provider":"115","enabled":true}],"nextAfter":"a","hasMore":true}`)
				} else {
					fmt.Fprint(w, body)
				}
			}))
			defer server.Close()
			result, err := DiscoverAccounts(context.Background(), server.URL, "fixture-secret")
			if err == nil || len(result.Items) != 0 || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("invalid page returned a partial list or leaked key")
			}
		})
	}
}

func TestDiscoverAccountsRejectsRedirectsAndRedactsErrors(t *testing.T) {
	var forwarded atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer destination.Close()
	for _, status := range []int{302, 401, 403, 404, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", destination.URL)
			w.WriteHeader(status)
			fmt.Fprint(w, "fixture-secret sensitive provider response")
		}))
		_, err := DiscoverAccounts(context.Background(), server.URL, "fixture-secret")
		server.Close()
		if err == nil || strings.Contains(err.Error(), "fixture-secret") || forwarded.Load() != 0 {
			t.Fatal("redirect followed or error response leaked")
		}
	}
}
