package agent

import (
	"encoding/json"
	"testing"
)

func TestDecodeArgsAndID(t *testing.T) {
	var a struct {
		ID   ID     `json:"id"`
		Name string `json:"name"`
	}
	for _, in := range []string{`{"id":12,"name":"x"}`, `{"id":"12","name":"x"}`} {
		a.ID = 0
		if err := DecodeArgs(json.RawMessage(in), &a); err != nil || a.ID != 12 || a.Name != "x" {
			t.Errorf("DecodeArgs(%s) = %+v, %v", in, a, err)
		}
	}
	for _, in := range []string{`{"id":0}`, `{"id":-3}`, `{"id":"abc"}`, `{"id":1.5}`, `{"id":null,"x":1}`, `{"id":1,"extra":true}`, `[]`, `{`} {
		if err := DecodeArgs(json.RawMessage(in), &a); err == nil {
			t.Errorf("DecodeArgs(%s) accepted", in)
		}
	}
}
