package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
)

// Files the owner and she pass each other in threads live in the lab, under FilesDir, and travel
// over SSH: they never rest in the Hekate container.
const (
	// FilesDir holds what the owner sends: FilesDir/in/<file id>/<name>.
	FilesDir = "/srv/files"
	// MaxFile is the largest file sent either way.
	MaxFile = 2 << 30
	// KeepFree is the room an upload must leave on the lab's disk.
	KeepFree = 4 << 30
	// fileLimit is how long one transfer may take: 2 GiB over a slow uplink.
	fileLimit = 45 * time.Minute
)

// Errors a file transfer can end with, for the caller to tell the owner apart.
var (
	ErrLabFull    = errors.New("the lab is nearly out of disk space")
	ErrNoFile     = errors.New("there's no such file in the lab")
	ErrNotRegular = errors.New("that isn't a regular file")
	ErrTruncated  = errors.New("the upload stopped before the whole file arrived")
)

// FileInfo is what's known of a file in the lab.
type FileInfo struct {
	Size    int64
	ModTime int64 // Unix seconds
}

// Upload writes what r holds (size bytes) to FilesDir/in/<id>/<name> in the lab, as her user, and
// returns its path. A transfer that stops early leaves nothing behind. name must be a plain file
// name and id a file id: the caller makes them so.
func (s *Service) Upload(ctx context.Context, id, name string, size int64, r io.Reader) (string, error) {
	if !plainName(id) || !plainName(name) {
		return "", fmt.Errorf("upload: bad file name %q", short(name, 80))
	}
	if size < 0 || size > MaxFile {
		return "", fmt.Errorf("upload: %d bytes is over the limit of %d", size, MaxFile)
	}
	if err := s.reserve(ctx, size); err != nil {
		return "", err
	}
	defer s.release(size)
	dir := path.Join(FilesDir, "in", id)
	dest := path.Join(dir, name)
	part := dest + ".part"
	n := strconv.FormatInt(size, 10)
	cmd := "sudo install -d -m 0755 " + quote(FilesDir) + " " + quote(path.Dir(dir)) +
		" && sudo install -d -o " + User + " -g " + User + " -m 0755 " + quote(dir) +
		" && cat > " + quote(part) +
		" && if [ \"$(stat -c %s " + quote(part) + ")\" = " + n + " ]; then mv -f " + quote(part) + " " + quote(dest) +
		"; else rm -f " + quote(part) + "; exit 3; fi"
	out := newCapWriter(4 << 10)
	exit, timedOut, err := s.exec(ctx, s.hekate(), cmd, io.LimitReader(r, size+1), out, out, fileLimit)
	if err != nil || exit != 0 {
		// Whatever stopped it, nothing half-written stays.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = s.shell(cctx, "rm -rf -- "+quote(dir), nil, 20*time.Second, 1024)
	}
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("upload: %w", ctx.Err()) // the owner's device went away
	case err != nil:
		return "", fmt.Errorf("upload: %w", err)
	case timedOut:
		return "", fmt.Errorf("upload: stopped after %s", fileLimit)
	case exit == 3:
		return "", ErrTruncated
	case exit != 0:
		text, _ := out.result()
		return "", fmt.Errorf("upload: exit %d: %s", exit, strings.TrimSpace(text))
	}
	return dest, nil
}

// Stat says what's at p in the lab: a regular file's size and modification time.
func (s *Service) Stat(ctx context.Context, p string) (FileInfo, error) {
	if !path.IsAbs(p) || len(p) > maxPath || strings.ContainsAny(p, "\x00\n") {
		return FileInfo{}, fmt.Errorf("path %q must be an absolute file path", short(p, 80))
	}
	p = path.Clean(p)
	// Exit 2: nothing there.
	res, err := s.shell(ctx, "if ! sudo test -e "+quote(p)+"; then exit 2; fi; sudo stat -L -c '%F|%s|%Y' -- "+quote(p),
		nil, 20*time.Second, 1024)
	if err != nil {
		return FileInfo{}, err
	}
	if res.Exit == 2 {
		return FileInfo{}, ErrNoFile
	}
	if res.Exit != 0 {
		return FileInfo{}, fmt.Errorf("stat %s: exit %d: %s", p, res.Exit, strings.TrimSpace(res.Output))
	}
	parts := strings.Split(strings.TrimSpace(res.Output), "|")
	if len(parts) != 3 {
		return FileInfo{}, fmt.Errorf("stat %s: can't read %q", p, short(res.Output, 80))
	}
	if parts[0] != "regular file" && parts[0] != "regular empty file" {
		return FileInfo{}, ErrNotRegular
	}
	size, err1 := strconv.ParseInt(parts[1], 10, 64)
	mtime, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return FileInfo{}, fmt.Errorf("stat %s: can't read %q", p, short(res.Output, 80))
	}
	return FileInfo{Size: size, ModTime: mtime}, nil
}

// Download writes the file at p in the lab to w. It doesn't check what it sends: Stat first.
func (s *Service) Download(ctx context.Context, p string, w io.Writer) error {
	var errOut bytes.Buffer
	exit, timedOut, err := s.exec(ctx, s.hekate(), "sudo cat -- "+quote(path.Clean(p)), nil, w,
		&limitWriter{w: &errOut, n: 1 << 10}, fileLimit)
	switch {
	case err != nil:
		return fmt.Errorf("download: %w", err)
	case timedOut:
		return fmt.Errorf("download: stopped after %s", fileLimit)
	case exit != 0:
		return fmt.Errorf("download: exit %d: %s", exit, strings.TrimSpace(clean(errOut.String())))
	}
	return nil
}

// reserve takes room for an upload of size bytes, leaving KeepFree on the lab's disk after it and
// every other upload in progress have landed. release gives it back.
func (s *Service) reserve(ctx context.Context, size int64) error {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	free, err := s.freeBytes(ctx, FilesDir)
	if err != nil {
		return err
	}
	// Counting the others in full errs on the safe side: what they've written already shows as used.
	if free-s.uploading-size < KeepFree {
		return ErrLabFull
	}
	s.uploading += size
	return nil
}

func (s *Service) release(size int64) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	s.uploading -= size
}

// freeBytes is the room left on the lab's disk where dir is (or would be).
func (s *Service) freeBytes(ctx context.Context, dir string) (int64, error) {
	// /srv/files may not exist yet: ask about the nearest folder that does.
	res, err := s.shell(ctx, "d="+quote(dir)+"; while [ ! -e \"$d\" ]; do d=$(dirname \"$d\"); done; df -B1 --output=avail \"$d\" | tail -n 1",
		nil, 20*time.Second, 1024)
	if err != nil {
		return 0, err
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(res.Output), 10, 64)
	if res.Exit != 0 || perr != nil {
		return 0, fmt.Errorf("can't tell how much room the lab has (exit %d): %s", res.Exit, short(res.Output, 120))
	}
	return n, nil
}

// plainName reports whether name is one file name: no folders, nothing hidden or odd.
func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && !strings.ContainsAny(name, "/\x00\n\r")
}

// limitWriter keeps the first n bytes written to it and drops the rest.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(l.n, len(p))
		if _, err := l.w.Write(p[:k]); err != nil {
			return 0, err
		}
		l.n -= k
	}
	return len(p), nil
}
