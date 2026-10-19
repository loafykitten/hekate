package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	type body struct {
		A string      `json:"a"`
		B Opt[string] `json:"b"`
	}
	tests := []struct {
		name     string
		body     string
		optional bool
		status   int // 0: ok
		check    func(t *testing.T, b body)
	}{
		{name: "ok", body: `{"a":"x"}`, check: func(t *testing.T, b body) {
			if b.A != "x" || b.B.Set {
				t.Errorf("got %+v", b)
			}
		}},
		{name: "null is set but empty", body: `{"b":null}`, check: func(t *testing.T, b body) {
			if !b.B.Set || b.B.Val != nil || b.B.Ptr() == nil || *b.B.Ptr() != "" {
				t.Errorf("got %+v", b)
			}
		}},
		{name: "value", body: `{"b":"v"}`, check: func(t *testing.T, b body) {
			if p := b.B.Ptr(); p == nil || *p != "v" {
				t.Errorf("got %+v", b)
			}
		}},
		{name: "unknown field", body: `{"zzz":1}`, status: 400},
		{name: "wrong type", body: `{"a":1}`, status: 400},
		{name: "trailing data", body: `{"a":"x"} {"a":"y"}`, status: 400},
		{name: "empty", body: ``, status: 400},
		{name: "empty optional", body: ``, optional: true},
		{name: "too large", body: `{"a":"` + strings.Repeat("x", MaxBody) + `"}`, status: 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
			w := httptest.NewRecorder()
			var b body
			dec := Decode
			if tt.optional {
				dec = DecodeOptional
			}
			err := dec(w, r, &b)
			if tt.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if tt.check != nil {
					tt.check(t, b)
				}
				return
			}
			var he *Error
			if !errors.As(err, &he) || he.Status != tt.status {
				t.Fatalf("err = %v, want status %d", err, tt.status)
			}
		})
	}
}

func TestFail(t *testing.T) {
	w := httptest.NewRecorder()
	Fail(w, nil, httptest.NewRequest(http.MethodGet, "/", nil), NotFound("task %d not found", 3))
	if w.Code != 404 || strings.TrimSpace(w.Body.String()) != `{"error":"task 3 not found"}` {
		t.Errorf("got %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	Fail(w, nil, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("secret detail"))
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("got %d %s", w.Code, w.Body)
	}
}

// Only JSON bodies: forms and text/plain are what a cross-site page can send
// without a CORS preflight.
func TestDecodeRequiresJSONContentType(t *testing.T) {
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1}`))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		var v struct{ A int }
		err := Decode(httptest.NewRecorder(), r, &v)
		var he *Error
		if !errors.As(err, &he) || he.Status != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: err = %v, want 415", ct, err)
		}
	}
}
