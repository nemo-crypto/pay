package web

import (
	"embed"
	"net/http"
)

//go:embed index.html app.js style.css onramp.html onramp.js vendor/qrcode.mjs
var files embed.FS

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "not found", 404)
			return
		}
		path := r.URL.Path
		file := "index.html"
		kind := "text/html; charset=utf-8"
		switch path {
		case "/onramp", "/onramp/checkout":
			file = "onramp.html"
		case "/onramp.js":
			file = "onramp.js"
			kind = "text/javascript; charset=utf-8"
		case "/app.js":
			file = "app.js"
			kind = "text/javascript; charset=utf-8"
		case "/vendor/qrcode.mjs":
			file = "vendor/qrcode.mjs"
			kind = "text/javascript; charset=utf-8"
		case "/style.css":
			file = "style.css"
			kind = "text/css; charset=utf-8"
		case "/", "/demo/checkout", "/top-up/success", "/top-up/cancel":
		default:
			http.NotFound(w, r)
			return
		}
		b, e := files.ReadFile(file)
		if e != nil {
			http.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", kind)
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		_, _ = w.Write(b)
	})
}
