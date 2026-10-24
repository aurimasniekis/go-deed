// Adapted from Porch internal/webui/webui.go (Apache-2.0).

// Package webui serves the embedded single-page application (web/ → internal/webui/dist). Hashed assets are immutable;
// every other path gets index.html with the request's CSP nonce injected into its <script> and <link> tags and a
// <meta name="deed-csp-nonce"> that Mantine uses for the <style> elements it creates.
package webui

import (
	"bytes"
	"embed"
	"html"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/aurimasniekis/go-deed/internal/httpx"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Hook runs before index.html is served for a page navigation; returning true means it handled the response
// (e.g. redirected to an upstream recheck).
type Hook func(w http.ResponseWriter, r *http.Request) bool

var (
	scriptTag = regexp.MustCompile(`<script\b`)
	linkTag   = regexp.MustCompile(`<link\b([^>]*\brel="(?:stylesheet|modulepreload)")`)
)

// injectNonce adds nonce attributes and the nonce meta tag.
func injectNonce(index []byte, nonce string) []byte {
	n := html.EscapeString(nonce)
	out := scriptTag.ReplaceAll(index, []byte(`<script nonce="`+n+`"`))
	out = linkTag.ReplaceAll(out, []byte(`<link nonce="`+n+`"$1`))
	return bytes.Replace(out, []byte("<head>"), []byte(`<head><meta name="deed-csp-nonce" content="`+n+`">`), 1)
}

// Handler serves files from files; hook (optional) runs for page navigations.
func Handler(files fs.FS, hook Hook) http.Handler {
	assets := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if strings.HasPrefix(p, "/assets/") || isStaticFile(files, p) {
			if _, err := fs.Stat(files, strings.TrimPrefix(p, "/")); err != nil {
				http.NotFound(w, r)
				return
			}
			if strings.HasPrefix(p, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			assets.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(files, "index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("The Deed web UI is not included in this build (run `make web` before building).\n"))
			return
		}
		if hook != nil && hook(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(injectNonce(index, httpx.Nonce(r.Context())))
	})
}

// isStaticFile reports top-level files such as /favicon.svg (but not index.html, which is served for every route).
func isStaticFile(files fs.FS, p string) bool {
	name := strings.TrimPrefix(p, "/")
	if name == "" || name == "index.html" || strings.Contains(name, "/") || !strings.Contains(name, ".") {
		return false
	}
	_, err := fs.Stat(files, name)
	return err == nil
}
