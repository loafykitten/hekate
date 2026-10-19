package mail

import "testing"

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		`"Boots from Example"`: "Boots from Example",
		`  Plain Name `:        "Plain Name",
		`"Say \"hi\" Co"`:      `Say "hi" Co`,
		`"`:                    `"`,
		`Half "quoted"`:        `Half "quoted"`,
		``:                     ``,
	} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}
