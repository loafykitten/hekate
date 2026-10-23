package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
)

// scripted serves the model's answers in order, and keeps what it was asked.
type scripted struct {
	mu      sync.Mutex
	answers [][]string
	asked   []string // each request's last message
}

func text(s string) []string {
	b, _ := json.Marshal(s)
	return []string{`{"choices":[{"index":0,"delta":{"content":` + string(b) + `}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "[DONE]"}
}

func inspect(cmd string) []string {
	args, _ := json.Marshal(fmt.Sprintf(`{"command":%q}`, cmd))
	return []string{fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c%d","type":"function",`+
		`"function":{"name":"lab_inspect","arguments":%s}}]}}]}`, len(cmd), args),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, "[DONE]"}
}

type lab struct {
	fail bool
	cmds []string
}

func (l *lab) ReviewerReady() bool { return true }

func (l *lab) Inspect(_ context.Context, c string) (string, error) {
	if l.fail {
		return "", errors.New("can't reach the lab")
	}
	if strings.HasPrefix(c, "rm") {
		return "Not run: that changes things.", nil
	}
	l.cmds = append(l.cmds, c)
	return "Exit status 0.", nil
}

func newReviewer(t *testing.T, s *scripted, l *lab) *Reviewer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		s.mu.Lock()
		s.asked = append(s.asked, req.Messages[len(req.Messages)-1].Content)
		answer := text("(out of script)")
		if len(s.answers) > 0 {
			answer, s.answers = s.answers[0], s.answers[1:]
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range answer {
			_, _ = io.WriteString(w, "data: "+line+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r, err := New(Options{Store: db, LLM: llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", MaxRetries: -1}),
		Model: config.Model{Name: "reviewer", Effort: "max"}, Lab: l})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunNumbersWhatRanAndAsksAgain(t *testing.T) {
	s := &scripted{answers: [][]string{
		inspect("rm -rf /opt"), inspect("cat compose.yaml"),
		text("Everything is fine."), // no JSON: asked once more
		text(`{"verdict":"needs_changes","summary":"One gap.","findings":[{"severity":"high","title":"t","evidence_call_ids":[1]}]}`),
	}}
	l := &lab{}
	rep, calls, err := newReviewer(t, s, l).Run(t.Context(), Request{Round: 1, Owner: []string{"set it up"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].ID != 1 || calls[0].Command != "cat compose.yaml" {
		t.Errorf("calls %+v: a refused command gets no id", calls)
	}
	if len(rep.Findings) != 1 || rep.Findings[0].Commands[0] != "cat compose.yaml" {
		t.Errorf("report %+v", rep)
	}
	if !strings.Contains(s.asked[1], "Not run") || !strings.HasPrefix(s.asked[2], "[call 1]") ||
		!strings.Contains(s.asked[3], "couldn't be read") {
		t.Errorf("what the reviewer was told: %q", s.asked)
	}
}

func TestRunGivesUpOnAnUnreachableLab(t *testing.T) {
	s := &scripted{answers: [][]string{inspect("ls"), inspect("ls -a"), inspect("ls -la")}}
	_, _, err := newReviewer(t, s, &lab{fail: true}).Run(t.Context(), Request{Round: 1})
	if err == nil || !strings.Contains(err.Error(), "didn't answer") {
		t.Errorf("err %v", err)
	}
}
