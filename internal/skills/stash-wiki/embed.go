// Package stashwiki exposes the read-only files for Stash's bundled wiki skill.
package stashwiki

import (
	"embed"
	"io/fs"
)

// content contains documentation only; the skill ships no scripts or hooks.
//
//go:embed content/stash-wiki
var content embed.FS

// Files returns the immutable embedded skill filesystem.
func Files() fs.FS {
	skillFiles, err := fs.Sub(content, "content/stash-wiki")
	if err != nil {
		panic(err)
	}
	return skillFiles
}
