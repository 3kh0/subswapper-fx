package subswapper

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	now := time.Now().UTC()
	primary := UsageSnapshot{
		FiveHour: LimitWindow{Pct: PtrFloat64(4), ResetsAt: now.Add(4*time.Hour + 57*time.Minute)},
		Weekly:   LimitWindow{Pct: PtrFloat64(71), ResetsAt: now.Add(29 * time.Hour)},
	}
	second := UsageSnapshot{
		FiveHour: LimitWindow{Pct: PtrFloat64(67), ResetsAt: now.Add(90 * time.Minute)},
		Weekly:   LimitWindow{Pct: PtrFloat64(46), ResetsAt: now.Add(61 * time.Hour)},
	}
	routes := []codexProxyRoute{
		{Account: "primary", Score: primary.Score(), HeadroomPerHour: codexHeadroomPerHour(primary, now)},
		{Account: "second", Score: second.Score(), HeadroomPerHour: codexHeadroomPerHour(second, now)},
	}
	balanceCodexRoutes(routes, 1)
	if routes[0].Account != "primary" {
		t.Fatalf("soon-resetting primary route = %s", routes[0].Account)
	}
	routes[0].HeadroomPerHour = 0.0095
	routes[1].HeadroomPerHour = 0.0095
	balanceCodexRoutes(routes, 2)
	if routes[0].Account != "second" {
		t.Fatalf("second equal-headroom route = %s", routes[0].Account)
	}
	balanceCodexRoutes(routes, 3)
	if routes[0].Account != "primary" {
		t.Fatalf("first equal-headroom route = %s", routes[0].Account)
	}
	routes[0].SessionNearLimit = true
	balanceCodexRoutes(routes, 4)
	if routes[0].Account != "second" {
		t.Fatalf("session headroom route = %s", routes[0].Account)
	}
	routes[0].Exhausted = true
	balanceCodexRoutes(routes, 5)
	if routes[0].Account != "primary" {
		t.Fatalf("non-exhausted route = %s", routes[0].Account)
	}
}
