// Package webapp serves the web client's built files (web/dist, copied into
// the control-plane image; ADR-037) and the security headers every response
// from the control plane's HTTPS port carries (docs/WEB.md §6).
package webapp

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// DefaultDir is where the control-plane image keeps the built web client.
const DefaultDir = "/usr/share/linx/web"

// ContentSecurityPolicy allows only this origin: no inline scripts or
// styles, no other hosts for scripts, fonts or connections (the API, /sip
// and the events websocket are all same-origin), and no framing.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; media-src 'self' blob:; " +
	"worker-src 'self'; manifest-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// PermissionsPolicy lets this origin use the microphone and choose the
// speaker, and nothing else a page could ask for.
const PermissionsPolicy = "microphone=(self), speaker-selection=(self), camera=(), geolocation=(), " +
	"payment=(), usb=(), serial=(), bluetooth=(), hid=(), midi=(), display-capture=()"

// Headers adds the security headers to every response.
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=63072000")
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("Permissions-Policy", PermissionsPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// Handler serves files from files: hashed build output under /assets/ is
// cached for a year, every other file and the app's own routes (anything
// that isn't a file: /, /team, /setup/<token>, …) get index.html and are
// revalidated every time, so a new release shows up on the next load.
func Handler(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if fi, err := fs.Stat(files, name); err == nil && !fi.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				http.ServeFileFS(w, r, files, name)
				return
			}
			// A missing build file is a 404, not the app: a stale page
			// asking for an old hashed script must not get HTML back.
			if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		b, err := fs.ReadFile(files, "index.html")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "The web client isn't installed in this image.", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(b)
	})
}
