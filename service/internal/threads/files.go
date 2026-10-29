package threads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/lab"
	"github.com/loafykitten/hekate/service/internal/proto"
)

// Lab moves a thread's files in and out of her lab; lab.Service satisfies it.
type Lab interface {
	Upload(ctx context.Context, id, name string, size int64, r io.Reader) (string, error)
	Stat(ctx context.Context, path string) (lab.FileInfo, error)
	Download(ctx context.Context, path string, w io.Writer) error
}

const maxFileName = 120 // characters kept of a file's name

const fileColumns = `id, thread_id, turn_id, direction, name, size, path, created_at`

func (s *Service) scanFile(row interface{ Scan(...any) error }) (proto.ThreadFile, error) {
	var f proto.ThreadFile
	var created string
	if err := row.Scan(&f.ID, &f.ThreadID, &f.TurnID, &f.Direction, &f.Name, &f.Size, &f.Path, &created); err != nil {
		return proto.ThreadFile{}, err //nolint:wrapcheck // callers wrap
	}
	f.CreatedAt = s.display(created)
	return f, nil
}

// files returns a thread's files that match where (with args after the thread's id), oldest first.
func (s *Service) files(ctx context.Context, thread, where string, args ...any) ([]proto.ThreadFile, error) {
	//nolint:gosec // where is a fixed condition from this file; values go through args
	rows, err := s.db.QueryContext(ctx, `SELECT `+fileColumns+` FROM thread_files WHERE thread_id = ? AND `+where+
		` ORDER BY created_at, rowid`, append([]any{thread}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("thread files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []proto.ThreadFile{}
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, fmt.Errorf("thread files: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("thread files: %w", err)
	}
	return out, nil
}

// turnFiles returns the files of the given turns, oldest first.
func (s *Service) turnFiles(ctx context.Context, thread string, turns []any) ([]proto.ThreadFile, error) {
	if len(turns) == 0 {
		return []proto.ThreadFile{}, nil
	}
	return s.files(ctx, thread, `turn_id IN (?`+strings.Repeat(", ?", len(turns)-1)+`)`, turns...)
}

func (s *Service) addFile(ctx context.Context, f proto.ThreadFile, mtime int64) (proto.ThreadFile, error) {
	now := stamp(s.now())
	if _, err := s.db.ExecContext(ctx, `INSERT INTO thread_files (id, thread_id, turn_id, direction, name, size, mtime, path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, f.ID, f.ThreadID, f.TurnID, f.Direction, f.Name, f.Size, mtime, f.Path, now); err != nil {
		return proto.ThreadFile{}, fmt.Errorf("record a thread file: %w", err)
	}
	f.CreatedAt = s.display(now)
	return f, nil
}

// cleanName makes a name the owner's device gave a file safe to use as one file's name in the lab:
// no folders, no control characters, nothing hidden, not too long. It keeps the extension.
func cleanName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.TrimLeft(name, ".")
	if utf8.RuneCountInString(name) > maxFileName {
		ext := path.Ext(name)
		if utf8.RuneCountInString(ext) > 16 {
			ext = ""
		}
		r := []rune(strings.TrimSuffix(name, ext))
		name = string(r[:maxFileName-utf8.RuneCountInString(ext)]) + ext
	}
	return name
}

func newFileID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "f_" + hex.EncodeToString(b[:])
}

func noLab() error {
	return &httpx.Error{Status: http.StatusServiceUnavailable, Msg: "files go through her lab, and it isn't set up"}
}

// handleUpload streams a file into her lab as one the owner can send in this thread.
func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	f, err := s.upload(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, f)
}

func (s *Service) upload(r *http.Request) (proto.ThreadFile, error) {
	ctx := r.Context()
	if s.opts.Lab == nil {
		return proto.ThreadFile{}, noLab()
	}
	t, err := s.Get(ctx, r.PathValue("id"))
	if err != nil {
		return proto.ThreadFile{}, err
	}
	if t.Archived {
		return proto.ThreadFile{}, httpx.Conflict("thread %q is archived", t.ID)
	}
	name := cleanName(r.URL.Query().Get("name"))
	switch {
	case name == "":
		return proto.ThreadFile{}, httpx.BadRequest("the file needs a name (?name=)")
	case r.ContentLength < 0:
		return proto.ThreadFile{}, &httpx.Error{Status: http.StatusLengthRequired, Msg: "the upload needs a Content-Length"}
	case r.ContentLength > lab.MaxFile:
		return proto.ThreadFile{}, &httpx.Error{Status: http.StatusRequestEntityTooLarge,
			Msg: fmt.Sprintf("a file can be at most %d GB", lab.MaxFile>>30)}
	}
	f := proto.ThreadFile{ID: newFileID(), ThreadID: t.ID, Direction: proto.FileIn, Name: name, Size: r.ContentLength}
	f.Path, err = s.opts.Lab.Upload(ctx, f.ID, name, f.Size, r.Body)
	switch {
	case errors.Is(err, lab.ErrLabFull):
		return proto.ThreadFile{}, &httpx.Error{Status: http.StatusInsufficientStorage,
			Msg: "her lab is nearly out of disk space; ask her to clear some room first"}
	case errors.Is(err, lab.ErrTruncated):
		return proto.ThreadFile{}, httpx.BadRequest("the upload stopped before the whole file arrived")
	case err != nil:
		return proto.ThreadFile{}, err //nolint:wrapcheck // Fail logs it as is
	}
	info, err := s.opts.Lab.Stat(ctx, f.Path)
	if err != nil {
		return proto.ThreadFile{}, fmt.Errorf("check an upload: %w", err)
	}
	s.log.Info("threads: file uploaded", "thread", t.ID, "file", f.ID, "bytes", f.Size)
	return s.addFile(ctx, f, info.ModTime)
}

// handleDownload streams a thread's file out of her lab, if it's still the file it was.
func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.opts.Lab == nil {
		httpx.Fail(w, s.log, r, noLab())
		return
	}
	var (
		f     proto.ThreadFile
		mtime int64
	)
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT `+fileColumns+`, mtime FROM thread_files WHERE thread_id = ? AND id = ?`,
		r.PathValue("id"), r.PathValue("file")).Scan(&f.ID, &f.ThreadID, &f.TurnID, &f.Direction, &f.Name, &f.Size, &f.Path, &created, &mtime)
	if err != nil {
		httpx.Fail(w, s.log, r, httpx.NotFound("there is no file %q in this thread", r.PathValue("file")))
		return
	}
	info, err := s.opts.Lab.Stat(ctx, f.Path)
	gone := &httpx.Error{Status: http.StatusGone, Msg: f.Name + " is no longer in her lab."}
	switch {
	case errors.Is(err, lab.ErrNoFile), errors.Is(err, lab.ErrNotRegular):
		httpx.Fail(w, s.log, r, gone)
		return
	case err != nil:
		httpx.Fail(w, s.log, r, err)
		return
	case info.Size != f.Size || info.ModTime != mtime:
		gone.Msg = f.Name + " has changed in her lab since; ask her to share it again."
		httpx.Fail(w, s.log, r, gone)
		return
	}
	h := w.Header()
	h.Set("Content-Length", fmt.Sprint(f.Size))
	if kind := imageExts[strings.ToLower(path.Ext(f.Name))]; r.URL.Query().Get("inline") == "1" && kind != "" && f.Size <= maxImageBytes {
		// An image to show in the page. A file id never names other bytes (a changed file is
		// refused above), so the browser may keep it.
		h.Set("Content-Type", kind)
		h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": f.Name}))
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
		h.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	if err := s.opts.Lab.Download(ctx, f.Path, w); err != nil && ctx.Err() == nil {
		// The headers are gone: a short body is how the client learns it failed.
		s.log.Warn("threads: download failed", "thread", f.ThreadID, "file", f.ID, "error", err)
	}
}

// SayWithFiles implements server.FileInput: words with the files (uploaded to this thread, not
// yet sent) they came with.
func (c *conv) SayWithFiles(origin, inputID, text string, files []string, _ bool) {
	if code, msg := c.say(origin, inputID, text, files); msg != "" {
		c.s.hub.Broadcast(proto.NewThreadEvent(c.id, proto.NewError(code, msg, inputID)))
	}
}

// unsent returns the files named, which must be this thread's uploads that no message has sent
// (or is sending). It runs under the service's lock, as say does.
func (c *conv) unsent(ctx context.Context, ids []string) ([]proto.ThreadFile, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	fs, err := c.s.files(ctx, c.id, `direction = 'in' AND turn_id = '' AND id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sending := range c.sending {
		fs = slices.DeleteFunc(fs, func(f proto.ThreadFile) bool {
			return slices.ContainsFunc(sending, func(g proto.ThreadFile) bool { return g.ID == f.ID })
		})
	}
	if len(fs) != len(ids) {
		return nil, errors.New("those files aren't waiting to be sent in this thread")
	}
	return fs, nil
}

// filesNote tells her where the files sent with a message are; she reads it after the owner's words.
func filesNote(tamer string, fs []proto.ThreadFile) string {
	what := "files"
	if len(fs) == 1 {
		what = "a file"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "(%s sent %s with this message, now in your lab:", tamer, what)
	for _, f := range fs {
		fmt.Fprintf(&b, "\n- %s (%s", f.Path, humanSize(f.Size))
		if isImage(f.Name) {
			b.WriteString("; an image: look_at_image shows it to you")
		}
		b.WriteString(")")
	}
	b.WriteString(")")
	return b.String()
}

// claimFiles ties the files a message sent to the turn it started, as that turn starts, so its
// turn_start and the saved history carry them.
func (c *conv) claimFiles(m *proto.TurnStart) {
	c.mu.Lock()
	fs, ok := c.sending[m.InputID]
	delete(c.sending, m.InputID)
	c.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.s.baseCtx), 5*time.Second)
	defer cancel()
	args := []any{m.TurnID, c.id}
	for _, f := range fs {
		args = append(args, f.ID)
	}
	//nolint:gosec // only placeholders are joined; every value is bound
	if _, err := c.s.db.ExecContext(ctx, `UPDATE thread_files SET turn_id = ? WHERE thread_id = ? AND turn_id = '' AND id IN (?`+
		strings.Repeat(", ?", len(fs)-1)+`)`, args...); err != nil {
		c.s.log.Error("threads: can't tie files to their turn", "thread", c.id, "turn", m.TurnID, "error", err)
		return
	}
	for i := range fs {
		fs[i].TurnID = m.TurnID
	}
	m.Files = fs
}

// dropSending forgets the files of a message that never started a turn: they can be sent again.
func (c *conv) dropSending(inputID string) {
	c.mu.Lock()
	delete(c.sending, inputID)
	c.mu.Unlock()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// shareTool lets her hand the owner a file from her lab, as a download under her reply.
func (s *Service) shareTool() agent.Tool {
	return agent.Tool{
		Name: "lab_share_file",
		Description: "Give the owner a file from your lab: it shows as a download under your reply in this thread. It " +
			"must be one regular file of at most 2 GB: zip a folder first (e.g. cd /srv/work/homebrew && zip -qr " +
			"/srv/files/out/homebrew.zip .). The download is the file as it is now; if you change it afterwards, share it again.",
		Schema: json.RawMessage(`{"type":"object","required":["path"],"properties":{
			"path":{"type":"string","description":"Absolute path of the file in your lab."},
			"name":{"type":"string","description":"The name it downloads as; default the file's own."}
		}}`),
		Effect: agent.EffectRead,
		Label:  "Sharing a file",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Path string `json:"path"`
				Name string `json:"name"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			turn := agent.TurnID(ctx)
			var thread string
			if err := s.db.QueryRowContext(ctx, `SELECT t.id FROM turns u JOIN threads t ON t.id = u.conversation_id WHERE u.id = ?`,
				turn).Scan(&thread); err != nil {
				return agent.Result{}, errors.New("files can only be shared in a thread")
			}
			info, err := s.opts.Lab.Stat(ctx, a.Path)
			switch {
			case errors.Is(err, lab.ErrNoFile):
				return agent.Result{}, fmt.Errorf("there's nothing at %s", a.Path)
			case errors.Is(err, lab.ErrNotRegular):
				return agent.Result{}, fmt.Errorf("%s isn't a regular file; zip a folder first", a.Path)
			case err != nil:
				return agent.Result{}, err //nolint:wrapcheck // her tool error, as is
			case info.Size > lab.MaxFile:
				return agent.Result{}, fmt.Errorf("%s is %s; at most 2 GB can be shared", a.Path, humanSize(info.Size))
			}
			name := cleanName(a.Name)
			if name == "" {
				name = cleanName(path.Base(path.Clean(a.Path)))
			}
			if name == "" {
				return agent.Result{}, errors.New("give the download a name")
			}
			f, err := s.addFile(ctx, proto.ThreadFile{ID: newFileID(), ThreadID: thread, TurnID: turn, Direction: proto.FileOut,
				Name: name, Size: info.Size, Path: path.Clean(a.Path)}, info.ModTime)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: fmt.Sprintf("Shared %s (%s) as %q: the owner sees a download under your reply.",
				f.Path, humanSize(f.Size), f.Name)}, nil
		},
	}
}
