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
	"/":                  "vue-console.html",
	"/index.html":        "vue-console.html",
	"/ui/goal-map":       "vue-console.html",
	"/ui/plan":           "vue-console.html",
	"/ui/monitor":        "vue-console.html",
	"/ui/monitor-vue":    "vue-console.html",
	"/ui/monitor-alpine": "vue-console.html",
	"/ui/issues":         "vue-console.html",
	"/ui/work-graph":     "vue-console.html",
	"/ui/git":            "vue-console.html",
	"/ui/namespaces":     "vue-console.html",
	"/ui/facts":          "vue-console.html",
	"/ui/hypotheses":     "vue-console.html",
	"/ui/goals":          "vue-console.html",
	"/ui/agent-guide":    "vue-console.html",
	"/ui/maintenance":    "vue-console.html",
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
