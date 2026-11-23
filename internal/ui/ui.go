// Package ui serves the demo web UI. It is a static, no-build page that calls
// the gateway's own API from the browser and holds no state on the server.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var assets embed.FS

// Prefix is where the UI is mounted. Register Handler at Prefix.
const Prefix = "/ui/"

// contentSecurityPolicy keeps scripts same-origin so nothing injected can read
// the access token the page keeps in sessionStorage. Avatars come from the
// Gitea host, so images may load over http(s); images cannot run script.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: http: https:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler serves the embedded UI files with security headers.
func Handler() http.Handler {
	static, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // the embedded directory always exists
	}
	files := http.StripPrefix(Prefix, http.FileServerFS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
