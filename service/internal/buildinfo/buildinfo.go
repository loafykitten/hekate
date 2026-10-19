// Package buildinfo holds values stamped in at link time via -ldflags -X.
package buildinfo

// Set by the Makefile and the image build; defaults apply to plain `go run`.
var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)
