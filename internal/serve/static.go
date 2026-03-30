package serve

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed pwa/*
var pwaFS embed.FS

// pwaHandler returns an http.Handler that serves the embedded PWA static
// assets. The "pwa" directory prefix is stripped so that requests to "/" serve
// pwa/index.html, "/style.css" serves pwa/style.css, etc.
//
// The service worker (sw.js) must be served from the root scope for proper
// caching control, which this setup provides.
//
// PWA static assets are served without bearer token authentication because
// the browser needs to fetch the manifest, service worker, and app shell
// before the user can enter credentials. All API endpoints still require auth.
func pwaHandler() http.Handler {
	sub, err := fs.Sub(pwaFS, "pwa")
	if err != nil {
		// This can only happen if the embed path is wrong, which is a build-time
		// error. Panic is appropriate here.
		panic("serve: failed to create sub-filesystem for PWA assets: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}
