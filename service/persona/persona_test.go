package persona

import (
	"strings"
	"testing"
)

func TestSection(t *testing.T) {
	doc := "# Title\n\n## One\n\nfirst\n\n## Two\n\nsecond\n"
	if got := Section(doc, "One"); got != "## One\n\nfirst" {
		t.Errorf("One = %q", got)
	}
	if got := Section(doc, "Two"); got != "## Two\n\nsecond" {
		t.Errorf("Two = %q", got)
	}
	if got := Section("## Lead\nx\n## Next\ny", "Lead"); got != "## Lead\nx" {
		t.Errorf("Lead = %q", got)
	}
	if Section(doc, "Missing") != "" || Section(doc, "On") != "" {
		t.Error("found a section that isn't there")
	}
}

// The thread prompt is built from these sections; renaming one must not drop it silently.
func TestThreadSectionsExist(t *testing.T) {
	for _, h := range []string{"Who you are", "Moments"} {
		if !strings.Contains(Section(Renamon, h), "\n") {
			t.Errorf("renamon.md has no %q section", h)
		}
	}
	if !strings.Contains(ThreadLab, "{{lab}}") || !strings.Contains(Thread, "{{tamer}}") {
		t.Error("thread prompts lost their placeholders")
	}
}
