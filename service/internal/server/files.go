package server

import (
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
)

// serveFile serves f from root with a size+mtime ETag. ServeContent handles
// conditional and range requests.
func serveFile(w http.ResponseWriter, r *http.Request, f *os.File, st fs.FileInfo, name, cacheControl string) {
	h := w.Header()
	h.Set("ETag", fmt.Sprintf(`"%x-%x"`, st.Size(), st.ModTime().UnixNano()))
	h.Set("Cache-Control", cacheControl)
	if ct := contentType(name); ct != "" {
		h.Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".glb":
		return "model/gltf-binary"
	case ".json":
		return "application/json"
	case ".webmanifest":
		return "application/manifest+json"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	}
	return mime.TypeByExtension(path.Ext(name)) // "" lets ServeContent sniff
}

// openRegular opens name inside dir via os.Root, which refuses to follow paths
// out of the directory, and requires a regular file.
func openRegular(dir, name string) (*os.File, fs.FileInfo, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open root: %w", err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fs.ErrNotExist
	}
	return f, st, nil
}

// handleCharacter serves only manifest.json and top-level .glb files; the
// asset directory may hold renders and sources that must never be exposed.
func (s *Server) handleCharacter(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !allowedCharacterFile(name) {
		http.NotFound(w, r)
		return
	}
	f, st, err := openRegular(s.assetsDir, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	serveFile(w, r, f, st, name, "no-cache")
}

func allowedCharacterFile(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return false
	}
	return name == "manifest.json" || strings.HasSuffix(name, ".glb")
}

// handleStatic serves the built PWA with an SPA fallback.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.webDir == "" || reservedPrefix(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	rel := strings.TrimPrefix(clean, "/")
	for seg := range strings.SplitSeq(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			http.NotFound(w, r)
			return
		}
	}

	if rel != "" {
		if f, st, err := openRegular(s.webDir, rel); err == nil {
			defer func() { _ = f.Close() }()
			serveFile(w, r, f, st, rel, staticCacheControl(rel))
			return
		}
	}
	// Only extensionless paths are client-side routes; a missing .js is a real 404.
	if path.Ext(clean) != "" {
		http.NotFound(w, r)
		return
	}
	f, st, err := openRegular(s.webDir, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	serveFile(w, r, f, st, "index.html", "no-cache")
}

func staticCacheControl(rel string) string {
	base := path.Base(rel)
	switch {
	case strings.HasPrefix(rel, "assets/"):
		return "public, max-age=31536000, immutable" // Vite content-hashes these
	case base == "index.html", base == "sw.js", base == "manifest.webmanifest",
		base == "registerSW.js", strings.HasPrefix(base, "workbox-") && strings.HasSuffix(base, ".js"):
		return "no-cache"
	}
	return "public, max-age=3600"
}
