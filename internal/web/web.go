package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var assets embed.FS

func Handler(mode string) http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/licenses" || r.URL.Path == "/settings" || r.URL.Path == "/rooms" {
			body, err := fs.ReadFile(root, "index.html")
			if err != nil {
				http.Error(w, "Build the embedded UI first", 503)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(bytes.Replace(body, []byte("<html"), []byte("<html data-app=\""+mode+"\""), 1))
			return
		}
		files.ServeHTTP(w, r)
	})
}
