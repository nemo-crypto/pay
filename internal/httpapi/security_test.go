package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDemoRejectsReboundHostBeforeIssuingToken(t *testing.T) {
	f := setup(t)
	for _, host := range []string{"attacker.example", "localhost.attacker.example", "localhost:9000"} {
		r := httptest.NewRequest("POST", "http://"+host+"/demo/session", nil)
		// X-Forwarded-Host 不得绕过实际 Host 校验。
		r.Header.Set("X-Forwarded-Host", "localhost")
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "token") {
			t.Fatalf("host %s: %d %s", host, w.Code, w.Body.String())
		}
	}
	for _, host := range []string{"localhost", "LOCALHOST"} {
		r := httptest.NewRequest("POST", "http://"+host+"/demo/session", nil)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "token") {
			t.Fatalf("configured host rejected: %d", w.Code)
		}
	}
}
func TestSecurityHeadersOnUnauthorizedAndLiveResponses(t *testing.T) {
	f := setup(t)
	f.token = ""
	for _, mode := range []string{"demo", "live"} {
		f.svc.Config.Mode = mode
		w := f.call("GET", "/me", "", "")
		if w.Code != 401 || w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Header())
		}
		if (w.Header().Get("Strict-Transport-Security") != "") != (mode == "live") {
			t.Fatal("HSTS mode mismatch")
		}
	}
}
func TestTopupLimiterCapacityPreservesExistingUsers(t *testing.T) {
	f := setup(t)
	now := time.Now()
	for i := 0; i < 10000; i++ {
		f.api.limits[fmt.Sprint(i)] = bucket{Start: now, Count: 1}
	}
	for i := 0; i < 20; i++ {
		if f.api.limit("extra"+fmt.Sprint(i), 10) {
			t.Fatal("unbounded limiter")
		}
	}
	if len(f.api.limits) != 10000 || !f.api.limit("0", 10) {
		t.Fatal("capacity must not grow or block existing users")
	}
	for k, b := range f.api.limits {
		b.Start = now.Add(-2 * time.Minute)
		f.api.limits[k] = b
	}
	// Simulate the sweep clock advancing together with expired bucket timestamps.
	f.api.nextSweep = now.Add(-time.Second)
	if !f.api.limit("new", 10) {
		t.Fatal("expired slots not released")
	}
}
