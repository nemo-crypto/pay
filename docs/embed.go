package docs

import (
	"embed"
	"net/http"
)

//go:embed providers/*.md
var tutorials embed.FS

// Handler 提供离线教程，浏览器直接查看并复制命令；不加载外部脚本。
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		names := map[string]string{"/guides/stripe": "stripe", "/guides/allscale": "allscale", "/guides/onramp-api": "onramp-api", "/guides/moonpay": "moonpay", "/guides/alchemypay": "alchemypay", "/guides/transak": "transak", "/guides/ramp": "ramp", "/guides/": "README"}
		name, ok := names[r.URL.Path]
		if !ok || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		b, err := tutorials.ReadFile("providers/" + name + ".md")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
	})
}
