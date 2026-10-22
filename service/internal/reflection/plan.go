package reflection

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// plan is what the model decided. Fields are pointers where "not given" and
// "empty" mean different things.
type plan struct {
	Save   []saveOp   `json:"save"`
	Update []updateOp `json:"update"`
	Retire []retireOp `json:"retire"`
	Diary  string     `json:"diary"`

	// updatesCut is set when the plan had more updates than are applied:
	// one of the dropped ones may be a merge's destination.
	updatesCut bool
}

type saveOp struct {
	Kind       string      `json:"kind"`
	Content    string      `json:"content"`
	Importance int         `json:"importance"`
	FollowUpAt string      `json:"follow_up_at"`
	SourceTurn flexStrings `json:"source_turns"`
	Reason     string      `json:"reason"`
}

type updateOp struct {
	ID         flexID      `json:"id"`
	Content    *string     `json:"content"`
	Kind       *string     `json:"kind"`
	Importance *int        `json:"importance"`
	FollowUpAt *string     `json:"follow_up_at"`
	MergedFrom []flexID    `json:"merged_from"` // a merge: the memories whose words this one now holds
	SourceTurn flexStrings `json:"source_turns"`
	Reason     string      `json:"reason"`
}

type retireOp struct {
	ID     flexID `json:"id"`
	Reason string `json:"reason"`
}

// flexStrings accepts a turn id or a list of them.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*f = flexStrings{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("source turns: %w", err)
	}
	*f = many
	return nil
}

// flexID accepts a memory id as a number or a string, as models write both.
type flexID int64

func (f *flexID) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexID(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("memory id: %w", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("memory id %q is not a whole number", s)
	}
	*f = flexID(n)
	return nil
}

// parsePlan reads the model's JSON answer, tolerating a code fence or words
// around the object. The diary is required; everything else may be empty.
func parsePlan(text string) (plan, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return plan{}, errors.New("the model's answer held no JSON object")
	}
	var p plan
	if err := json.Unmarshal([]byte(text[start:end+1]), &p); err != nil {
		return plan{}, fmt.Errorf("the model's answer wasn't valid JSON: %w", err)
	}
	p.Diary = strings.TrimSpace(p.Diary)
	if p.Diary == "" {
		return plan{}, errors.New("the model wrote no diary entry")
	}
	p.Diary = clip(p.Diary, maxDiary)
	p.Save = p.Save[:min(len(p.Save), maxSaves)]
	if len(p.Update) > maxUpdates {
		p.Update, p.updatesCut = p.Update[:maxUpdates], true
	}
	p.Retire = p.Retire[:min(len(p.Retire), maxRetires)]
	return p, nil
}
