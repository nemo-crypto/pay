package docs

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

//go:embed providers/*.md integration.md guide.html guide.css guide.js
var tutorials embed.FS

type guideLink struct {
	Title, URL string
	Current    bool
}
type guidePage struct {
	Title, RawURL string
	Links         []guideLink
	Content       template.HTML
}

var guideNames = map[string]string{"wechat": "微信支付", "stripe": "Stripe", "allscale": "Allscale", "moonpay": "MoonPay", "alchemypay": "Alchemy Pay", "transak": "Transak", "ramp": "Ramp", "onramp-api": "买币 API 说明", "integration": "接入与运维", "README": "支付教程总览"}

// Handler 将内嵌 Markdown 渲染为离线阅读页；?format=raw 保留可复制的原文接口。
func Handler() http.Handler {
	tpl := template.Must(template.ParseFS(tutorials, "guide.html"))
	md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/guides/guide.css" || r.URL.Path == "/guides/guide.js" {
			file := "guide.css"
			kind := "text/css; charset=utf-8"
			if strings.HasSuffix(r.URL.Path, ".js") {
				file = "guide.js"
				kind = "text/javascript; charset=utf-8"
			}
			b, _ := tutorials.ReadFile(file)
			w.Header().Set("Content-Type", kind)
			if r.Method != "HEAD" {
				_, _ = w.Write(b)
			}
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/guides/")
		if name == "" {
			name = "README"
		}
		name = strings.TrimSuffix(name, ".md")
		title, ok := guideNames[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		path := "providers/" + name + ".md"
		if name == "integration" {
			path = "integration.md"
		}
		source, err := tutorials.ReadFile(path)
		if err != nil {
			http.Error(w, "guide unavailable", 500)
			return
		}
		if r.URL.Query().Get("format") == "raw" || strings.HasSuffix(r.URL.Path, ".md") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if r.Method != "HEAD" {
				_, _ = w.Write(source)
			}
			return
		}
		doc := md.Parser().Parse(text.NewReader(source))
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if entering {
				if link, ok := n.(*ast.Link); ok {
					dest := string(link.Destination)
					parts := strings.SplitN(dest, "#", 2)
					target := strings.TrimPrefix(parts[0], "../")
					target = strings.TrimPrefix(target, "providers/")
					target = strings.TrimSuffix(target, ".md")
					if _, ok := guideNames[target]; ok {
						dest = "/guides/" + target
						if target == "README" {
							dest = "/guides/"
						}
						if len(parts) > 1 {
							dest += "#" + parts[1]
						}
						link.Destination = []byte(dest)
					}
				}
			}
			return ast.WalkContinue, nil
		})
		var rendered bytes.Buffer
		if err = md.Renderer().Render(&rendered, source, doc); err != nil {
			http.Error(w, "guide unavailable", 500)
			return
		}
		links := []guideLink{}
		for _, id := range []string{"wechat", "stripe", "allscale", "moonpay", "alchemypay", "transak", "ramp"} {
			links = append(links, guideLink{guideNames[id], "/guides/" + id, id == name})
		}
		var page bytes.Buffer
		// Content 仅来自禁用原始 HTML 的 Markdown renderer，模板其余字段继续自动转义。
		if err = tpl.Execute(&page, guidePage{title, fmt.Sprintf("/guides/%s?format=raw", name), links, template.HTML(rendered.String())}); err != nil {
			http.Error(w, "guide unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != "HEAD" {
			_, _ = w.Write(page.Bytes())
		}
	})
}
