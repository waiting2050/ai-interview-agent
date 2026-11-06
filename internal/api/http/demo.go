package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
)

// The demo page is a single static page served by the API process. There is no
// frontend build step: index.html, app.js and style.css are embedded at compile
// time.
//
//go:embed demo/index.html demo/app.js demo/style.css
var demoAssets embed.FS

func (h *handlers) demoPage(w http.ResponseWriter, _ *http.Request) {
	body, err := demoAssets.ReadFile("demo/index.html")
	if err != nil {
		h.deps.Logger.Error("read demo asset", "error", err)
		http.Error(w, "demo unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func demoStatic() http.Handler {
	sub, err := fs.Sub(demoAssets, "demo")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/demo/", http.FileServer(http.FS(sub)))
}
