package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name   string
		db     Pinger
		status int
		ok     bool
		dbWant string
	}{
		{"ok", fakeDB{}, 200, true, "ok"},
		{"db down", fakeDB{err: errors.New("disk on fire")}, 503, false, "disk on fire"},
		{"no db", nil, 503, false, "not configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(Options{DB: tt.db}).Routes()
			rec := do(t, h, "GET", "/healthz")
			if rec.Code != tt.status {
				t.Fatalf("status = %d", rec.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["ok"] != tt.ok || body["db"] != tt.dbWant {
				t.Errorf("body = %v", body)
			}
			for _, k := range []string{"version", "commit", "built_at", "started_at", "uptime_s"} {
				if _, ok := body[k]; !ok {
					t.Errorf("missing %s", k)
				}
			}
		})
	}
}

func TestSecurityHeadersAndRequestID(t *testing.T) {
	h := New(Options{DB: fakeDB{}}).Routes()
	rec := do(t, h, "GET", "/healthz")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers: %v", rec.Header())
	}
	if len(rec.Header().Get("X-Request-Id")) < 8 {
		t.Errorf("request id: %q", rec.Header().Get("X-Request-Id"))
	}

	req := httptest.NewRequest("GET", "/healthz", http.NoBody)
	req.Header.Set("X-Request-Id", "abc-123")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") != "abc-123" {
		t.Errorf("inbound id not honoured")
	}
	req.Header.Set("X-Request-Id", "bad id\twith junk")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") == "bad id\twith junk" {
		t.Errorf("unsafe id accepted")
	}
}

func TestAccessLogOmitsQuery(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := New(Options{Logger: log, DB: fakeDB{}}).Routes()
	do(t, h, "GET", "/api/nothing?token=SECRET")
	out := buf.String()
	if strings.Contains(out, "SECRET") || strings.Contains(out, "token") {
		t.Fatalf("query leaked: %s", out)
	}
	for _, want := range []string{`"path":"/api/nothing"`, `"status":404`, `"method":"GET"`, `"request_id"`, `"duration_ms"`, `"bytes"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestPanicRecovery(t *testing.T) {
	var buf bytes.Buffer
	s := New(Options{Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") }),
		s.requestID, s.accessLog, s.recoverer, secureHeaders)
	rec := do(t, h, "GET", "/x")
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(buf.String(), "kaboom") || !strings.Contains(buf.String(), `"status":500`) {
		t.Errorf("log: %s", buf.String())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("headers lost on panic")
	}
}

func TestAPINotFoundJSON(t *testing.T) {
	// Even with a web dir, /api/* must not fall back to the SPA.
	web := t.TempDir()
	write(t, filepath.Join(web, "index.html"), "<html>spa</html>")
	h := New(Options{DB: fakeDB{}, WebDir: web}).Routes()
	for _, method := range []string{"GET", "POST"} {
		for _, p := range []string{"/api/unknown", "/api/", "/api/a/b"} {
			rec := do(t, h, method, p)
			if rec.Code != 404 || strings.TrimSpace(rec.Body.String()) != `{"error":"not found"}` {
				t.Errorf("%s %s: %d %q", method, p, rec.Code, rec.Body.String())
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Errorf("%s %s content-type %q", method, p, rec.Header().Get("Content-Type"))
			}
		}
	}
}

func assetServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "manifest.json"), `{"ok":true}`)
	write(t, filepath.Join(dir, "model.glb"), "GLBDATA")
	write(t, filepath.Join(dir, ".hidden.glb"), "x")
	write(t, filepath.Join(dir, "notes.txt"), "x")
	write(t, filepath.Join(dir, "renders", "a.glb"), "x")
	write(t, filepath.Join(filepath.Dir(dir), "secret.glb"), "outside")
	return New(Options{DB: fakeDB{}, AssetsDir: dir}).Routes(), dir
}

func TestCharacterServing(t *testing.T) {
	h, _ := assetServer(t)

	rec := do(t, h, "GET", "/character/model.glb")
	if rec.Code != 200 || rec.Body.String() != "GLBDATA" {
		t.Fatalf("glb: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "model/gltf-binary" {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
		t.Errorf("caching headers: %v", rec.Header())
	}

	rec = do(t, h, "GET", "/character/manifest.json")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("manifest: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// Conditional request returns 304 via the ETag.
	etag := rec.Header().Get("ETag")
	req := httptest.NewRequest("GET", "/character/manifest.json", http.NoBody)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("conditional = %d", rec2.Code)
	}

	if rec := do(t, h, "HEAD", "/character/model.glb"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d len %d", rec.Code, rec.Body.Len())
	}
}

func TestCharacterRejections(t *testing.T) {
	h, _ := assetServer(t)
	for _, p := range []string{
		"/character/notes.txt",
		"/character/renders/a.glb",
		"/character/renders",
		"/character/.hidden.glb",
		"/character/missing.glb",
		"/character/..%2Fsecret.glb",
		"/character/%2e%2e%2fsecret.glb",
		"/character/..%5csecret.glb",
		"/character/..",
		"/character/",
		"/character/manifest.json%00.glb",
	} {
		rec := do(t, h, "GET", p)
		if rec.Code == 200 {
			t.Errorf("%s served (200): %q", p, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "outside") {
			t.Errorf("%s leaked outside file", p)
		}
	}
	if rec := do(t, h, "GET", "/character/renders/a.glb"); rec.Code != 404 {
		t.Errorf("subdir = %d", rec.Code)
	}
}

func TestCharacterDisabledWithoutDir(t *testing.T) {
	h := New(Options{DB: fakeDB{}}).Routes()
	if rec := do(t, h, "GET", "/character/manifest.json"); rec.Code != 404 {
		t.Errorf("status = %d", rec.Code)
	}
}

func webServer(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "index.html"), "<html>spa</html>")
	write(t, filepath.Join(dir, "sw.js"), "sw")
	write(t, filepath.Join(dir, "registerSW.js"), "reg")
	write(t, filepath.Join(dir, "workbox-abc123.js"), "wb")
	write(t, filepath.Join(dir, "manifest.webmanifest"), "{}")
	write(t, filepath.Join(dir, "favicon.svg"), "<svg/>")
	write(t, filepath.Join(dir, "assets", "index-Dk3j.js"), "bundle")
	write(t, filepath.Join(dir, ".env"), "SECRET=1")
	return New(Options{DB: fakeDB{}, WebDir: dir}).Routes()
}

func TestStatic(t *testing.T) {
	h := webServer(t)
	const immutable = "public, max-age=31536000, immutable"
	tests := []struct {
		name, path, body, cache string
		code                    int
	}{
		{"root", "/", "<html>spa</html>", "no-cache", 200},
		{"index", "/index.html", "<html>spa</html>", "no-cache", 200},
		{"hashed asset", "/assets/index-Dk3j.js", "bundle", immutable, 200},
		{"sw", "/sw.js", "sw", "no-cache", 200},
		{"registerSW", "/registerSW.js", "reg", "no-cache", 200},
		{"workbox", "/workbox-abc123.js", "wb", "no-cache", 200},
		{"webmanifest", "/manifest.webmanifest", "{}", "no-cache", 200},
		{"other file", "/favicon.svg", "<svg/>", "public, max-age=3600", 200},
		{"spa route", "/menu/settings", "<html>spa</html>", "no-cache", 200},
		{"missing asset", "/assets/nope.js", "", "", 404},
		{"missing ext file", "/nope.png", "", "", 404},
		{"dotfile", "/.env", "", "", 404},
		{"dot dir", "/.git/config", "", "", 404},
		{"healthz-like not spa", "/healthzz", "<html>spa</html>", "no-cache", 200},
		{"character no assets", "/character/x", "", "", 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, "GET", tt.path)
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d", rec.Code, tt.code)
			}
			if tt.code != 200 {
				return
			}
			if rec.Body.String() != tt.body {
				t.Errorf("body = %q", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Errorf("cache-control = %q, want %q", got, tt.cache)
			}
		})
	}
}

func TestStaticTraversal(t *testing.T) {
	parent := t.TempDir()
	web := filepath.Join(parent, "web")
	write(t, filepath.Join(web, "index.html"), "spa")
	write(t, filepath.Join(parent, "outside.txt"), "outside")
	if err := os.Symlink(filepath.Join(parent, "outside.txt"), filepath.Join(web, "link.txt")); err != nil {
		t.Skip("symlinks unavailable")
	}
	h := New(Options{DB: fakeDB{}, WebDir: web}).Routes()
	for _, p := range []string{"/../outside.txt", "/..%2foutside.txt", "/link.txt", "/%2e%2e/outside.txt"} {
		rec := do(t, h, "GET", p)
		if rec.Body.String() == "outside" {
			t.Errorf("%s leaked", p)
		}
	}
}

func TestNoWebDir(t *testing.T) {
	h := New(Options{DB: fakeDB{}}).Routes()
	if rec := do(t, h, "GET", "/"); rec.Code != 404 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSPAFallbackNeedsIndex(t *testing.T) {
	h := New(Options{DB: fakeDB{}, WebDir: t.TempDir()}).Routes()
	if rec := do(t, h, "GET", "/somewhere"); rec.Code != 404 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestStatusWriterPassesThroughBody(t *testing.T) {
	rec := do(t, New(Options{DB: fakeDB{}}).Routes(), "GET", "/healthz")
	if b, _ := io.ReadAll(rec.Body); len(b) == 0 {
		t.Error("empty body")
	}
}
