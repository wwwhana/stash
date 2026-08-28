// Package stashwork exposes the read-only files for Stash's bundled work skill.
package stashwork

import (
	"embed"
	"io/fs"
)

// content contains documentation only. In particular, the skill does not ship
// scripts, hooks, or any other host-side execution surface.
//
//go:embed content/stash-work
var content embed.FS

// Files returns the immutable embedded skill filesystem.
func Files() fs.FS {
	skillFiles, err := fs.Sub(content, "content/stash-work")
	if err != nil {
		panic(err)
	}
	return skillFiles
}
