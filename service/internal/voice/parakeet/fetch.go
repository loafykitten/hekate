package parakeet

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// ModelDirName is the folder under the models directory that holds the model.
	ModelDirName = "parakeet-tdt-0.6b-v2-int8"

	// defaultArchiveURL is the sherpa-onnx release of NVIDIA Parakeet TDT 0.6B v2
	// (English, int8). The archive is 482468385 bytes; its hash is pinned so a
	// swapped or truncated download is never extracted into place.
	defaultArchiveURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2"
	defaultArchiveSHA256 = "157c157bc51155e03e37d2466522a3a737dd9c72bb25f36eb18912964161e1ad"

	markerName = ".complete"
)

// Files the recognizer needs, by their base name in the archive.
const (
	encoderFile = "encoder.int8.onnx"
	decoderFile = "decoder.int8.onnx"
	joinerFile  = "joiner.int8.onnx"
	tokensFile  = "tokens.txt"
)

var neededFiles = []string{encoderFile, decoderFile, joinerFile, tokensFile}

// modelComplete reports whether dir holds a finished download.
func modelComplete(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, markerName)); err != nil {
		return false
	}
	for _, f := range neededFiles {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// safeEntryName rejects absolute paths and any ".." component, then returns the
// cleaned slash-separated name.
func safeEntryName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", fmt.Errorf("absolute archive entry %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive entry %q escapes the target", name)
		}
	}
	return path.Clean(name), nil
}

// checkSHA256 compares a finished hash with the expected hex digest.
func checkSHA256(h hash.Hash, want string) error {
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, strings.ToLower(want))
	}
	return nil
}

// extractModel unpacks the needed files from a tar stream into dest (flat, by
// base name). Every entry name is validated, including the ones skipped.
func extractModel(tr *tar.Reader, dest string) error {
	found := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name, err := safeEntryName(hdr.Name)
		if err != nil {
			return err
		}
		base := path.Base(name)
		if hdr.Typeflag != tar.TypeReg || !isNeeded(base) {
			continue
		}
		if err := writeFile(filepath.Join(dest, base), tr); err != nil {
			return err
		}
		found[base] = true
	}
	for _, f := range neededFiles {
		if !found[f] {
			return fmt.Errorf("archive is missing %s", f)
		}
	}
	return nil
}

func isNeeded(base string) bool {
	for _, f := range neededFiles {
		if f == base {
			return true
		}
	}
	return false
}

func writeFile(p string, r io.Reader) (err error) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(p), err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close %s: %w", filepath.Base(p), cerr)
		}
	}()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(p), err)
	}
	return nil
}

// progressReader logs download progress at info, at most every few seconds.
type progressReader struct {
	r     io.Reader
	total int64
	read  atomic.Int64
	log   *slog.Logger
	every time.Duration
	last  time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	read := p.read.Add(int64(n))
	if now := time.Now(); now.Sub(p.last) >= p.every {
		p.last = now
		if p.total > 0 {
			p.log.Info("downloading speech model", "mb", read>>20, "of_mb", p.total>>20, "percent", read*100/p.total)
		} else {
			p.log.Info("downloading speech model", "mb", read>>20)
		}
	}
	return n, err
}

// download fetches the archive, streams it through bzip2 and tar into a temp
// directory next to dest, verifies the pinned checksum of the compressed bytes
// and only then renames the directory into place. Nothing unverified is ever
// visible at dest.
func (t *Transcriber) download(ctx context.Context, dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, ".parakeet-download-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.archiveURL, nil) //nolint:gosec // G704: the URL is a pinned constant (a test server in tests)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := t.http.Do(req) //nolint:gosec // G704: see NewRequestWithContext above
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: http %d", resp.StatusCode)
	}

	h := sha256.New()
	body := io.TeeReader(&progressReader{r: resp.Body, total: resp.ContentLength, log: t.log, every: t.progressEvery}, h)
	if err := extractModel(tar.NewReader(bzip2.NewReader(body)), tmp); err != nil {
		return err
	}
	// bzip2 and tar stop at their own end markers; hash whatever trails them.
	if _, err := io.Copy(io.Discard, body); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := checkSHA256(h, t.archiveSHA256); err != nil {
		return err
	}

	// The marker goes in before the rename so the directory is complete the
	// instant it appears.
	if err := os.WriteFile(filepath.Join(tmp, markerName), []byte(t.archiveSHA256+"\n"), 0o600); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	if err := os.RemoveAll(dest); err != nil { // a partial leftover from an older crash
		return fmt.Errorf("clear old model dir: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("install model: %w", err)
	}
	return nil
}
