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

var uiPagePaths = map[string]struct{}{
	"/":               {},
	"/index.html":     {},
	"/ui/goal-map":    {},
	"/ui/plan":        {},
	"/ui/monitor":     {},
	"/ui/issues":      {},
	"/ui/work-graph":  {},
	"/ui/git":         {},
	"/ui/namespaces":  {},
	"/ui/facts":       {},
	"/ui/hypotheses":  {},
	"/ui/goals":       {},
	"/ui/agent-guide": {},
	"/ui/maintenance": {},
}

// GetUIHandler returns the HTTP handler for the embedded UI files.
func GetUIHandler() http.Handler {
	subFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(subFS, "index.html")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(subFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path == "" {
			path = "/"
		}
		if _, ok := uiPagePaths[path]; !ok {
			files.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}
