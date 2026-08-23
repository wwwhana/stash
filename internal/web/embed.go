package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/*
var uiFS embed.FS

// GetUIHandler returns the HTTP handler for the embedded UI files.
func GetUIHandler() http.Handler {
	subFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(subFS))
}
