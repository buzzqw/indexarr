package adminapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestProxyCRUD(t *testing.T) {
	e := newEnv(t, true, true)
	created := e.do(http.MethodPost, "/api/v2/proxies", `{
		"kind": "http",
		"url": "http://user:pass-SECRET@127.0.0.1:8080"
	}`)
	requireStatus(t, created, http.StatusCreated)
	if strings.Contains(string(created.body), "pass-SECRET") || strings.Contains(string(created.body), "user:") {
		t.Fatalf("proxy response leaked credentials: %s", created.body)
	}
	proxy := decodeJSON[proxyResponse](t, created.body)
	if proxy.ID == 0 || proxy.URL != "http://127.0.0.1:8080" || !proxy.HasAuth {
		t.Fatalf("proxy = %+v", proxy)
	}

	list := e.do(http.MethodGet, "/api/v2/proxies", "")
	requireStatus(t, list, http.StatusOK)
	var rows []proxyResponse
	rows = decodeJSON[[]proxyResponse](t, list.body)
	if len(rows) != 1 || rows[0].ID != proxy.ID {
		t.Fatalf("list = %+v", rows)
	}

	updated := e.do(http.MethodPut, "/api/v2/proxies/"+itoa64(proxy.ID), `{
		"url": "socks5://127.0.0.1:1080",
		"clear_auth": true
	}`)
	requireStatus(t, updated, http.StatusOK)
	after := decodeJSON[proxyResponse](t, updated.body)
	if after.Kind != "socks5" || after.HasAuth || after.URL != "socks5://127.0.0.1:1080" {
		t.Fatalf("updated = %+v", after)
	}

	deleted := e.do(http.MethodDelete, "/api/v2/proxies/"+itoa64(proxy.ID), "")
	requireStatus(t, deleted, http.StatusNoContent)
	missing := e.do(http.MethodGet, "/api/v2/proxies/"+itoa64(proxy.ID), "")
	requireStatus(t, missing, http.StatusNotFound)
}

func TestProxyRejectsBadScheme(t *testing.T) {
	e := newEnv(t, true, true)
	res := e.do(http.MethodPost, "/api/v2/proxies", `{"url":"socks4://127.0.0.1:1080"}`)
	requireStatus(t, res, http.StatusBadRequest)
}

func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
