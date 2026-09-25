package subswapper

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFXBearerTracksRefreshedSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatgpt-auth.json")
	t.Setenv("SUBSWAPPER_FX_AUTH_FILE", path)
	proxy := &CodexProxy{placeholder: CodexProxyPlaceholder{Token: "proxy-secret"}}
	check := func(token string) bool {
		req := httptest.NewRequest("GET", "/subswapper/health", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return proxy.authorized(req)
	}
	if err := os.WriteFile(path, []byte(`{"access_token":"first"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !check("first") || check("wrong") {
		t.Fatal("initial fx token was not checked")
	}
	if err := os.WriteFile(path, []byte(`{"access_token":"second"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if check("first") || !check("second") || !check("proxy-secret") {
		t.Fatal("refreshed fx or native proxy token was not checked")
	}
}

func TestBalanceCodexRoutes(t *testing.T) {
	routes := []codexProxyRoute{
		{Account: "primary", Score: 0.21},
		{Account: "second", Score: 0.20},
	}
	balanceCodexRoutes(routes, 1)
	if routes[0].Account != "primary" {
		t.Fatalf("first close-score route = %s", routes[0].Account)
	}
	balanceCodexRoutes(routes, 2)
	if routes[0].Account != "second" {
		t.Fatalf("second close-score route = %s", routes[0].Account)
	}
	routes[0].Score = 0.70
	balanceCodexRoutes(routes, 3)
	if routes[0].Account != "primary" {
		t.Fatalf("least-used route = %s", routes[0].Account)
	}
	routes[0].Exhausted = true
	balanceCodexRoutes(routes, 4)
	if routes[0].Account != "second" {
		t.Fatalf("non-exhausted route = %s", routes[0].Account)
	}
}
