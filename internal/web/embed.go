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
		setBrowserSecurityHeaders(w)
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

func setBrowserSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; form-action 'self'; frame-src 'none'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
