// Package web holds the user interface. DSM serves these files itself; the embedded copy is used
// by the standalone listener (development, or running outside DSM).
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the UI files rooted at the static directory.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
