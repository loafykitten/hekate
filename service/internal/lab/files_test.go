package lab

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fileLab answers the commands file transfers send: df, the upload, stat and cat.
func fileLab(free string, got *[]byte, uploadExit int, stat string, statExit int, body string) func(string, []byte) (string, int) {
	return func(cmd string, stdin []byte) (string, int) {
		switch {
		case strings.Contains(cmd, "df -B1"):
			return free + "\n", 0
		case strings.Contains(cmd, "cat > "):
			*got = stdin
			return "", uploadExit
		case strings.Contains(cmd, "stat -L"):
			return stat, statExit
		case strings.HasPrefix(cmd, "sudo cat -- "):
			return body, 0
		}
		return "", 0
	}
}

func TestUploadStreamsIntoTheLab(t *testing.T) {
	f := newFixture(t)
	var got []byte
	f.lab.handle = fileLab("53687091200", &got, 0, "", 0, "")
	content := "PK\x03\x04 a zip"
	p, err := f.svc.Upload(t.Context(), "f_1", "homebrew.zip", int64(len(content)), strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if p != "/srv/files/in/f_1/homebrew.zip" || string(got) != content {
		t.Fatalf("path %q, stdin %q", p, got)
	}
	var upload string
	for _, c := range f.lab.commands() {
		if strings.Contains(c, "cat > ") {
			upload = c
		}
	}
	for _, want := range []string{"-o hekate -g hekate", "'/srv/files/in/f_1'", "= 10 ]", "mv -f '/srv/files/in/f_1/homebrew.zip.part' '/srv/files/in/f_1/homebrew.zip'"} {
		if !strings.Contains(upload, want) {
			t.Errorf("upload command lacks %q:\n%s", want, upload)
		}
	}
}

func TestUploadRefusals(t *testing.T) {
	f := newFixture(t)
	var got []byte
	f.lab.handle = fileLab("5368709120", &got, 0, "", 0, "") // 5 GiB free
	if _, err := f.svc.Upload(t.Context(), "f_1", "big.pkg", 2<<30, strings.NewReader("")); !errors.Is(err, ErrLabFull) {
		t.Fatalf("2 GiB into 5 GiB free: %v", err)
	}
	if _, err := f.svc.Upload(t.Context(), "f_1", "huge.pkg", MaxFile+1, strings.NewReader("")); err == nil {
		t.Fatal("over the limit was taken")
	}
	for _, name := range []string{"", "..", "a/b", "x\ny"} {
		if _, err := f.svc.Upload(t.Context(), "f_1", name, 1, strings.NewReader("x")); err == nil {
			t.Errorf("name %q was taken", name)
		}
	}
	if got != nil {
		t.Fatal("a refused upload reached the lab")
	}
}

func TestUploadCutShortLeavesNothing(t *testing.T) {
	f := newFixture(t)
	var got []byte
	f.lab.handle = fileLab("53687091200", &got, 3, "", 0, "")
	_, err := f.svc.Upload(t.Context(), "f_2", "a.zip", 100, strings.NewReader("only part"))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v", err)
	}
	cmds := f.lab.commands()
	if last := cmds[len(cmds)-1]; last != "rm -rf -- '/srv/files/in/f_2'" {
		t.Fatalf("last command %q", last)
	}
}

func TestStatAndDownload(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = fileLab("", nil, 0, "regular file|2048|1761600000\n", 0, "the bytes")
	info, err := f.svc.Stat(t.Context(), "/srv/ps5/out.zip")
	if err != nil || info != (FileInfo{Size: 2048, ModTime: 1761600000}) {
		t.Fatalf("stat = %+v, %v", info, err)
	}
	var b bytes.Buffer
	if err := f.svc.Download(t.Context(), "/srv/ps5/out.zip", &b); err != nil || b.String() != "the bytes" {
		t.Fatalf("download = %q, %v", b.String(), err)
	}

	f.lab.handle = fileLab("", nil, 0, "directory|4096|1761600000\n", 0, "")
	if _, err := f.svc.Stat(t.Context(), "/srv/ps5"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("a folder: %v", err)
	}
	f.lab.handle = fileLab("", nil, 0, "", 2, "")
	if _, err := f.svc.Stat(t.Context(), "/srv/gone.zip"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("nothing there: %v", err)
	}
	if _, err := f.svc.Stat(t.Context(), "relative/x"); err == nil {
		t.Fatal("a relative path was taken")
	}
}

// Uploads at once can't, together, leave the lab with less than KeepFree.
func TestUploadsTogetherKeepRoom(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{}, 2)
	unblock := make(chan struct{})
	f.lab.handle = func(cmd string, _ []byte) (string, int) {
		switch {
		case strings.Contains(cmd, "df -B1"):
			return "7516192768\n", 0 // 7 GiB: room for one 2 GiB upload, not two
		case strings.Contains(cmd, "cat > "):
			started <- struct{}{}
			<-unblock
		}
		return "", 0
	}
	const size = 2 << 30
	first := make(chan error, 1)
	go func() {
		_, err := f.svc.Upload(t.Context(), "f_a", "a.pkg", size, strings.NewReader(""))
		first <- err
	}()
	<-started
	if _, err := f.svc.Upload(t.Context(), "f_b", "b.pkg", size, strings.NewReader("")); !errors.Is(err, ErrLabFull) {
		t.Fatalf("second upload while the first runs: %v", err)
	}
	close(unblock)
	if err := <-first; err != nil {
		t.Fatalf("first upload: %v", err)
	}
	f.lab.handle = func(cmd string, _ []byte) (string, int) {
		if strings.Contains(cmd, "df -B1") {
			return "8589934592\n", 0
		}
		return "", 0
	}
	if _, err := f.svc.Upload(t.Context(), "f_c", "c.zip", 0, strings.NewReader("")); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
	f.svc.uploadMu.Lock()
	defer f.svc.uploadMu.Unlock()
	if f.svc.uploading != 0 {
		t.Fatalf("%d bytes still reserved", f.svc.uploading)
	}
}
