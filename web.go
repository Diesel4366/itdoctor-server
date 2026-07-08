package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFiles embed.FS

// SetupWebRoutes добавляет веб-интерфейс к существующему mux
func SetupWebRoutes(mux *http.ServeMux) {
	// Отдаём статику из embed
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(staticFS))

	// Корневой роут → index.html
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			data, err := staticFiles.ReadFile("static/index.html")
			if err != nil {
				http.Error(w, "index.html not found", 500)
				return
			}
			w.Write(data)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
