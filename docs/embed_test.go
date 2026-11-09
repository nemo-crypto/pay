package docs

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOfflineGuidesAndRawCompatibility(t *testing.T) {
	h := Handler()
	for _, name := range []string{"", "stripe", "allscale", "moonpay", "alchemypay", "transak", "ramp", "onramp-api", "integration"} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/guides/"+name, nil))
			body := w.Body.String()
			if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, "<h1") || !strings.Contains(body, "<pre><code") {
				t.Fatalf("guide %s not rendered: %d", name, w.Code)
			}
			if strings.Contains(body, `href="stripe.md"`) || strings.Contains(body, `href="../integration.md"`) {
				t.Fatal("unresolved local link")
			}
			raw := httptest.NewRecorder()
			h.ServeHTTP(raw, httptest.NewRequest("GET", "/guides/"+name+"?format=raw", nil))
			if raw.Code != 200 || !strings.HasPrefix(raw.Body.String(), "# ") || !strings.Contains(raw.Header().Get("Content-Type"), "text/plain") {
				t.Fatal("raw source unavailable")
			}
		})
	}
	for _, path := range []string{"/guides/guide.css", "/guides/guide.js", "/guides/stripe.md"} {
		get := httptest.NewRecorder()
		h.ServeHTTP(get, httptest.NewRequest("GET", path, nil))
		head := httptest.NewRecorder()
		h.ServeHTTP(head, httptest.NewRequest("HEAD", path, nil))
		if get.Code != 200 || head.Code != 200 || head.Body.Len() != 0 || get.Header().Get("Content-Security-Policy") == "" {
			t.Fatal(path, "asset/HEAD failure")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/guides/unknown", nil))
	if w.Code != 404 {
		t.Fatal("unknown guide accepted")
	}
}
func TestGuidesConcurrentRendering(t *testing.T) {
	h := Handler()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/guides/transak", nil))
			if w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	wg.Wait()
}
