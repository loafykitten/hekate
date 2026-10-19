package parakeet

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/voice"
)

// fixtureB64 is a tiny tar.bz2 shaped like the real archive: a top folder with
// the four needed files (contents "enc", "dec", "join", "a 0\n") plus a README
// that must be skipped.
const (
	fixtureB64 = "QlpoOTFBWSZTWTtf2gkAAQxfgMmwQAH/wCYDFEFuO95ACDgwANjbEapoeomBpMAACMBoGinpGpgEwANR6TTQxNAikmpponojam01MQ08kZGJtMez1ur0BZC0pXzvKBDDyVUu2XNvQaEr1A+uWZ7FA+Y5xUgBiITjGZ3PX+HcmUEIpGQK8DC/j90iN8BrszLJtlLgRTE9aTJJPRsqqNaJVAQN9ERhMzn0eGhtMgPvQE30DNHdw0cKVwoLMtwwZsX7q8NZHb5LNBCA6OCUSYAk9iDYbe8IKXCSdWfEZtdogeBdyRThQkDtf2gk"
	fixtureSHA = "ca2a39b8b9a06935ab26d5ae4980757ac042f3bed43c9cf134734c52c0f4a692"
)

func tarOf(t *testing.T, entries map[string]string) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return tar.NewReader(&buf)
}

func TestSafeEntryName(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../x", "a/../../x", "a/..", "", `a\..\b`, "a\x00b"} {
		if _, err := safeEntryName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for in, want := range map[string]string{"m/encoder.int8.onnx": "m/encoder.int8.onnx", "./m/tokens.txt": "m/tokens.txt", "m/": "m"} {
		got, err := safeEntryName(in)
		if err != nil || got != want {
			t.Errorf("safeEntryName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestExtractModelRejectsUnsafeAndIncomplete(t *testing.T) {
	good := map[string]string{"m/" + encoderFile: "e", "m/" + decoderFile: "d", "m/" + joinerFile: "j", "m/" + tokensFile: "t"}

	dir := t.TempDir()
	if err := extractModel(tarOf(t, good), dir); err != nil {
		t.Fatalf("good archive: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, tokensFile)); string(b) != "t" {
		t.Errorf("tokens = %q", b)
	}

	for name, entry := range map[string]string{"parent": "../evil", "absolute": "/tmp/evil", "nested parent": "m/../../evil"} {
		bad := map[string]string{entry: "x"}
		for k, v := range good {
			bad[k] = v
		}
		d := t.TempDir()
		if err := extractModel(tarOf(t, bad), d); err == nil {
			t.Errorf("%s: unsafe entry accepted", name)
		}
	}

	missing := map[string]string{"m/" + encoderFile: "e", "m/" + tokensFile: "t"}
	if err := extractModel(tarOf(t, missing), t.TempDir()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing files: %v", err)
	}
}

func TestCheckSHA256(t *testing.T) {
	h := sha256.New()
	_, _ = io.WriteString(h, "hello")
	const hello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if err := checkSHA256(h, hello); err != nil {
		t.Errorf("good hash rejected: %v", err)
	}
	if err := checkSHA256(h, strings.ToUpper(hello)); err != nil {
		t.Errorf("uppercase hex rejected: %v", err)
	}
	if err := checkSHA256(h, strings.Repeat("0", 64)); err == nil {
		t.Error("wrong hash accepted")
	}
}

func fixtureServer(t *testing.T, failFirst int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(fixtureB64)
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256.Sum256(raw); !strings.EqualFold(strings.TrimSpace(fixtureSHA), hexOf(got[:])) {
		t.Fatalf("fixture hash drifted: %s", hexOf(got[:]))
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= failFirst {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

type fakeEngine struct {
	text   string
	calls  atomic.Int32
	closed atomic.Bool
}

func (f *fakeEngine) decode(samples []float32) (string, error) {
	f.calls.Add(1)
	return "  " + f.text + " \n", nil
}
func (f *fakeEngine) close() { f.closed.Store(true) }

func fakeTranscriber(t *testing.T, srvURL, sha, modelsDir string, eng *fakeEngine) *Transcriber {
	t.Helper()
	tr := newTranscriber(modelsDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.archiveURL, tr.archiveSHA256 = srvURL, sha
	tr.retryBase, tr.retryMax = time.Millisecond, 5*time.Millisecond
	tr.load = func(dir string, _ int) (engine, error) {
		if !modelComplete(dir) {
			return nil, errors.New("model dir incomplete")
		}
		return eng, nil
	}
	return tr
}

func TestDownloadRetriesVerifiesAndInstalls(t *testing.T) {
	srv, calls := fixtureServer(t, 2)
	models := t.TempDir()
	eng := &fakeEngine{text: "hello there"}
	tr := fakeTranscriber(t, srv.URL, fixtureSHA, models, eng)

	pcm := make([]int16, 16000)
	for i := range pcm {
		pcm[i] = int16(1000 * (i%50 - 25))
	}
	if _, err := tr.Transcribe(t.Context(), pcm, 16000); !errors.Is(err, voice.ErrNotReady) {
		t.Fatalf("before prepare: %v", err)
	}

	tr.prepare(t.Context())
	if calls.Load() != 3 {
		t.Errorf("download attempts = %d, want 3 (2 failures then success)", calls.Load())
	}
	if !tr.Ready() {
		t.Fatal("not ready after prepare")
	}
	dir := filepath.Join(models, ModelDirName)
	if !modelComplete(dir) {
		t.Fatal("model dir not complete")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, joinerFile)); string(b) != "join" {
		t.Errorf("joiner = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "README")); err == nil {
		t.Error("unneeded README extracted")
	}
	left, _ := filepath.Glob(filepath.Join(models, ".parakeet-download-*"))
	if len(left) != 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}

	text, err := tr.Transcribe(t.Context(), pcm, 16000)
	if err != nil || text != "hello there" {
		t.Errorf("Transcribe = %q, %v", text, err)
	}
	// Silence and tiny clips never reach the engine.
	before := eng.calls.Load()
	if text, err := tr.Transcribe(t.Context(), make([]int16, 16000), 16000); err != nil || text != "" {
		t.Errorf("silence = %q, %v", text, err)
	}
	if eng.calls.Load() != before {
		t.Error("engine called for silence")
	}

	tr.Close()
	if !eng.closed.Load() || tr.Ready() {
		t.Error("Close did not release the engine")
	}
	if _, err := tr.Transcribe(t.Context(), pcm, 16000); !errors.Is(err, voice.ErrNotReady) {
		t.Errorf("after Close: %v", err)
	}
}

func TestDownloadRejectsWrongHash(t *testing.T) {
	srv, _ := fixtureServer(t, 0)
	models := t.TempDir()
	tr := fakeTranscriber(t, srv.URL, strings.Repeat("0", 64), models, &fakeEngine{})
	err := tr.download(t.Context(), filepath.Join(models, ModelDirName))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(models)
	if len(entries) != 0 {
		t.Errorf("failed download left %d entries behind", len(entries))
	}
}

func TestPrepareSkipsDownloadWhenComplete(t *testing.T) {
	srv, calls := fixtureServer(t, 0)
	models := t.TempDir()
	dir := filepath.Join(models, ModelDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range append([]string{markerName}, neededFiles...) {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tr := fakeTranscriber(t, srv.URL, fixtureSHA, models, &fakeEngine{})
	tr.prepare(t.Context())
	if calls.Load() != 0 || !tr.Ready() {
		t.Errorf("calls=%d ready=%v", calls.Load(), tr.Ready())
	}
}

func TestPrepareStopsOnCancel(t *testing.T) {
	srv, _ := fixtureServer(t, 1<<20) // always fails
	tr := fakeTranscriber(t, srv.URL, fixtureSHA, t.TempDir(), &fakeEngine{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	tr.prepare(ctx)
	if tr.Ready() {
		t.Error("ready despite failing download")
	}
}

func TestTranscribeWaitingCallerCanCancel(t *testing.T) {
	tr := fakeTranscriber(t, "", fixtureSHA, t.TempDir(), &fakeEngine{})
	tr.eng = &fakeEngine{}
	tr.ready.Store(true)
	<-tr.slot // simulate a decode in progress
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	pcm := make([]int16, 16000)
	for i := range pcm {
		pcm[i] = int16(1000 * (i%50 - 25))
	}
	if _, err := tr.Transcribe(ctx, pcm, 16000); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
