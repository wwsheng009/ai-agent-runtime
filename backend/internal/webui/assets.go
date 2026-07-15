package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

// embeddedFiles always contains at least dist/placeholder.txt so ordinary Go
// builds remain valid before the frontend packaging script has been run.
//
//go:embed dist
var embeddedFiles embed.FS

var distFiles = mustSub(embeddedFiles, "dist")

func mustSub(root fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(root, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// Available reports whether a production frontend has been embedded.
func Available() bool {
	return available(distFiles)
}

func available(assets fs.FS) bool {
	info, err := fs.Stat(assets, "index.html")
	return err == nil && info.Mode().IsRegular()
}

// Handler serves the embedded frontend and falls back to index.html for
// client-side routes.
func Handler() http.Handler {
	return newSPAHandler(distFiles)
}
