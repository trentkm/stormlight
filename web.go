package main

import (
	"embed"
	"io/fs"

	"github.com/trentkm/stormlight/internal/api"
)

// The built web client, compiled in so `stormlight serve` is one binary
// with nothing to install beside it.
//
// The blank line before the directive is not decoration: without a file
// present this fails the build, which is the point — a binary that
// silently shipped no page would look identical until someone opened it.
// `web/dist` is committed for that reason, and CI rebuilds it so a stale
// commit cannot survive a change to web/src.
//
//go:embed all:web/dist
var assets embed.FS

// webClient is the built page, rooted so paths match what the browser
// asks for.
func webClient() api.Assets {
	rooted, err := fs.Sub(assets, "web/dist")
	if err != nil {
		// Only reachable if the embed above changed shape.
		panic("web client assets are not where the embed says: " + err.Error())
	}
	return rooted
}
