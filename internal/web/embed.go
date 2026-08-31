package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed ui/*
var uiFS embed.FS

var uiPageFiles = map[string]string{
	"/":                  "index.html",
	"/index.html":        "index.html",
	"/ui/goal-map":       "index.html",
	"/ui/plan":           "index.html",
	"/ui/monitor":        "vue-monitor.html",
	"/ui/monitor-vue":    "vue-monitor.html",
	"/ui/monitor-alpine": "index.html",
	"/ui/issues":         "index.html",
	"/ui/work-graph":     "index.html",
	"/ui/git":            "index.html",
	"/ui/namespaces":     "index.html",
	"/ui/facts":          "index.html",
	"/ui/hypotheses":     "index.html",
	"/ui/goals":          "index.html",
	"/ui/agent-guide":    "index.html",
	"/ui/maintenance":    "index.html",
}

// GetUIHandler returns the HTTP handler for the embedded UI files.
func GetUIHandler() http.Handler {
	subFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(subFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path == "" {
			path = "/"
		}
		pageFile, ok := uiPageFiles[path]
		if !ok {
			files.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page, err := fs.ReadFile(subFS, pageFile)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		http.ServeContent(w, r, pageFile, time.Time{}, bytes.NewReader(page))
	})
}
