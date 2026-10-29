package threads

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/lab"
	"github.com/loafykitten/hekate/service/internal/proto"
)

// memLab is her lab's files, in memory.
type memLab struct {
	mu    sync.Mutex
	files map[string]memFile
}

type memFile struct {
	data  string
	mtime int64
}

func newMemLab() *memLab { return &memLab{files: map[string]memFile{}} }

func (l *memLab) put(p, data string, mtime int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[p] = memFile{data, mtime}
}

func (l *memLab) Upload(_ context.Context, id, name string, size int64, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if int64(len(b)) != size {
		return "", lab.ErrTruncated
	}
	p := path.Join(lab.FilesDir, "in", id, name)
	l.put(p, string(b), 1000)
	return p, nil
}

func (l *memLab) Stat(_ context.Context, p string) (lab.FileInfo, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.files[p]
	if !ok {
		return lab.FileInfo{}, lab.ErrNoFile
	}
	return lab.FileInfo{Size: int64(len(f.data)), ModTime: f.mtime}, nil
}

func (l *memLab) Download(_ context.Context, p string, w io.Writer) error {
	l.mu.Lock()
	f := l.files[p]
	l.mu.Unlock()
	_, err := io.WriteString(w, f.data)
	return err
}

func withLab(l *memLab) func(*Options) { return func(o *Options) { o.Lab = l } }

func (f *fixture) api(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func upload(t *testing.T, srv *httptest.Server, thread, name, body string) (int, proto.ThreadFile) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/threads/"+thread+"/files?name="+name, "application/zip", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var file proto.ThreadFile
	_ = json.NewDecoder(resp.Body).Decode(&file)
	return resp.StatusCode, file
}

func (f *fixture) sayFiles(t *testing.T, id, inputID, text string, files ...string) {
	t.Helper()
	in, err := f.svc.Input(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	in.(*conv).SayWithFiles("c_1", inputID, text, files, false)
}

func TestSendFiles(t *testing.T) {
	l := newMemLab()
	f := newFixture(t, withLab(l))
	srv := f.api(t)
	th, _ := f.svc.Create(t.Context(), "PS5", "", OriginOwner)

	code, up := upload(t, srv, th.ID, "ps5%20homebrew.zip", "PK zipped")
	if code != http.StatusCreated || up.Direction != proto.FileIn || up.Name != "ps5 homebrew.zip" || up.Size != 9 ||
		up.TurnID != "" || up.Path != "/srv/files/in/"+up.ID+"/ps5 homebrew.zip" {
		t.Fatalf("upload: %d %+v", code, up)
	}

	f.sayFiles(t, th.ID, "in-1", "Update everything in here", up.ID)
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 })

	starts := ofType[proto.TurnStart](f.hub.inner(th.ID))
	if len(starts) != 1 || len(starts[0].Files) != 1 || starts[0].Files[0].ID != up.ID || starts[0].Files[0].TurnID != starts[0].TurnID ||
		starts[0].UserText != "Update everything in here" {
		t.Fatalf("turn_start %+v", starts)
	}
	// She reads the owner's words and where the file is; the owner sees only their words.
	msgs := f.model.requests()[0].Messages
	last, _ := msgs[len(msgs)-1]["content"].(string)
	if !strings.HasPrefix(last, "Update everything in here\n\n(Sam sent a file") || !strings.Contains(last, up.Path+" (9 bytes)") {
		t.Errorf("she read %q", last)
	}
	page, _, files, err := f.svc.Messages(t.Context(), th.ID, 0, 10)
	if err != nil || page[0].Text != "Update everything in here" || len(files) != 1 || files[0].TurnID != starts[0].TurnID {
		t.Fatalf("history %+v, files %+v, %v", page, files, err)
	}

	// Sent once: sending it again, or another thread's file, is refused, and nothing starts.
	other, _ := f.svc.Create(t.Context(), "Other", "", OriginOwner)
	_, theirs := upload(t, srv, other.ID, "x.txt", "x")
	for _, id := range []string{up.ID, theirs.ID, "f_nope"} {
		f.sayFiles(t, th.ID, "in-"+id, "again", id)
	}
	errs := ofType[proto.Error](f.hub.inner(th.ID))
	if len(errs) != 3 || errs[0].Code != proto.CodeBadMessage || errs[2].InputID != "in-f_nope" {
		t.Fatalf("errors %+v", errs)
	}
	if n := len(ofType[proto.TurnStart](f.hub.inner(th.ID))); n != 1 {
		t.Fatalf("%d turns started", n)
	}
}

func TestUploadRefused(t *testing.T) {
	f := newFixture(t, withLab(newMemLab()))
	srv := f.api(t)
	th, _ := f.svc.Create(t.Context(), "T", "", OriginOwner)
	if code, _ := upload(t, srv, th.ID, "", "x"); code != http.StatusBadRequest {
		t.Errorf("no name: %d", code)
	}
	if code, _ := upload(t, srv, "th_nope", "a.zip", "x"); code != http.StatusNotFound {
		t.Errorf("unknown thread: %d", code)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/threads/"+th.ID+"/files?name=a.zip", strings.NewReader("x"))
	req.ContentLength = lab.MaxFile + 1
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("over 2 GiB: %d", resp.StatusCode)
		}
	}
	yes := true
	_, _ = f.svc.Update(t.Context(), th.ID, Patch{Archived: &yes})
	if code, _ := upload(t, srv, th.ID, "a.zip", "x"); code != http.StatusConflict {
		t.Errorf("archived: %d", code)
	}

	noLab := newFixture(t)
	th2, _ := noLab.svc.Create(t.Context(), "T", "", OriginOwner)
	if code, _ := upload(t, noLab.api(t), th2.ID, "a.zip", "x"); code != http.StatusServiceUnavailable {
		t.Errorf("no lab: %d", code)
	}
	share := func(t agent.Tool) bool { return t.Name == "lab_share_file" }
	if slices.ContainsFunc(noLab.svc.tools, share) || !slices.ContainsFunc(f.svc.tools, share) {
		t.Error("lab_share_file should come with the lab, and only then")
	}
}

func TestShareAndDownload(t *testing.T) {
	l := newMemLab()
	l.put("/srv/work/homebrew.zip", "updated zip", 2000)
	f := newFixture(t, withLab(l))
	srv := f.api(t)
	th, _ := f.svc.Create(t.Context(), "PS5", "", OriginOwner)
	f.model.script = func(req modelReq) []string {
		if last := req.Messages[len(req.Messages)-1]; last["role"] == "tool" {
			return reply("Here it is.")
		}
		return callTool("lab_share_file", `{"path":"/srv/work/homebrew.zip","name":"homebrew-updated.zip"}`)
	}
	f.say(t, th.ID, "send it back")
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 })

	_, steps, files, _ := f.svc.Messages(t.Context(), th.ID, 0, 10)
	if len(files) != 1 || files[0].Direction != proto.FileOut || files[0].Name != "homebrew-updated.zip" || files[0].Size != 11 ||
		files[0].TurnID != f.turnEnds(th.ID)[0].TurnID {
		t.Fatalf("files %+v (steps %+v)", files, steps)
	}
	get := func(thread string) (int, http.Header, string) {
		resp, err := http.Get(srv.URL + "/api/threads/" + thread + "/files/" + files[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}
	code, h, body := get(th.ID)
	if code != http.StatusOK || body != "updated zip" || h.Get("Content-Disposition") != `attachment; filename=homebrew-updated.zip` {
		t.Fatalf("download: %d %q %v", code, body, h)
	}
	l.put("/srv/work/homebrew.zip", "changed zip", 3000)
	if code, _, _ := get(th.ID); code != http.StatusGone {
		t.Errorf("changed since: %d", code)
	}
	l.mu.Lock()
	delete(l.files, "/srv/work/homebrew.zip")
	l.mu.Unlock()
	if code, _, body := get(th.ID); code != http.StatusGone || !strings.Contains(body, "no longer in her lab") {
		t.Errorf("removed: %d %s", code, body)
	}
	if code, _, _ := get("th_other"); code != http.StatusNotFound {
		t.Errorf("another thread's file: %d", code)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"homebrew.zip":                   "homebrew.zip",
		"  ../../etc/passwd":             "passwd",
		`C:\Users\me\ps5.zip`:            "ps5.zip",
		".hidden":                        "hidden",
		"a\nb\x00c.txt":                  "a_b_c.txt",
		"..":                             "",
		strings.Repeat("x", 200) + ".7z": strings.Repeat("x", 117) + ".7z",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookAtImage(t *testing.T) {
	l := newMemLab()
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64)
	l.put("/srv/files/in/f_1/pasted.png", png, 1000)
	l.put("/srv/files/in/f_1/notes.txt", "just text", 1000)
	f := newFixture(t, withLab(l), func(o *Options) { o.Vision = config.Model{Name: "vis-model", Effort: "low"} })
	srv := f.api(t)
	th, _ := f.svc.Create(t.Context(), "Error", "", OriginOwner)
	f.model.script = func(req modelReq) []string {
		if req.Model == "vis-model" {
			return reply("The dialog says: Payload failed (0x80020001).")
		}
		last := req.Messages[len(req.Messages)-1]
		if last["role"] != "tool" {
			return callTool("look_at_image", `{"path":"/srv/files/in/f_1/pasted.png","question":"What does the dialog say?"}`)
		}
		return reply("It's error 0x80020001.")
	}
	f.say(t, th.ID, "what's this error?")
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 })

	var vision, after *modelReq
	for _, r := range f.model.requests() {
		if r.Model == "vis-model" {
			vision = &r
		} else if r.Messages[len(r.Messages)-1]["role"] == "tool" {
			after = &r
		}
	}
	if vision == nil || after == nil {
		t.Fatal("no look, or no answer after it")
	}
	parts, _ := json.Marshal(vision.Messages[1]["content"])
	if !strings.Contains(string(parts), `"text":"What does the dialog say?"`) ||
		!strings.Contains(string(parts), `"url":"data:image/png;base64,`) {
		t.Errorf("vision request content %s", parts)
	}
	result, _ := after.Messages[len(after.Messages)-1]["content"].(string)
	if !strings.Contains(result, "Payload failed (0x80020001)") || !strings.Contains(result, "<untrusted_image_description") {
		t.Errorf("she read %q", result)
	}
	var n int
	_ = f.db.DB().QueryRow(`SELECT count(*) FROM usage WHERE model = 'vis-model' AND turn_id = ?`, f.turnEnds(th.ID)[0].TurnID).Scan(&n)
	if n != 1 {
		t.Errorf("%d usage rows for the look", n)
	}

	look := func() agent.Tool {
		for _, tool := range f.svc.tools {
			if tool.Name == "look_at_image" {
				return tool
			}
		}
		t.Fatal("no look_at_image")
		return agent.Tool{}
	}()
	if _, err := look.Run(t.Context(), json.RawMessage(`{"path":"/srv/files/in/f_1/notes.txt","question":"what?"}`)); err == nil ||
		!strings.Contains(err.Error(), "isn't a PNG") {
		t.Errorf("a text file: %v", err)
	}

	// The app shows images inline; other files and other asks download.
	_, img := upload(t, srv, th.ID, "shot.PNG", png)
	resp, err := http.Get(srv.URL + "/api/threads/" + th.ID + "/files/" + img.ID + "?inline=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline") ||
		!strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("inline image headers %v", resp.Header)
	}
	_, txt := upload(t, srv, th.ID, "notes.txt", "hello")
	resp, err = http.Get(srv.URL + "/api/threads/" + th.ID + "/files/" + txt.ID + "?inline=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("inline text headers %v", resp.Header)
	}
	if note := filesNote("Sam", []proto.ThreadFile{img, txt}); !strings.Contains(note, "shot.PNG (72 bytes; an image: look_at_image") ||
		strings.Contains(note, "notes.txt (5 bytes;") {
		t.Errorf("note %q", note)
	}
}
