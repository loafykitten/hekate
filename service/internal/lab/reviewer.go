package lab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ReviewerUser is the lab account the reviewer of her work looks around as. It can read what any
// user can and the journal, and run only read-only docker commands and the firewall listing as
// root (deploy/lab/vm/reviewer-setup.sh): it can't change the lab.
const ReviewerUser = "reviewer"

// Limits of one reviewer command.
const (
	InspectLimit  = 60 * time.Second
	inspectMaxOut = 8 << 10
)

// reviewerBin holds the reviewer's docker, which asks lab-docker-ro through sudo.
const reviewerBin = "/usr/local/lib/hekate-lab/reviewer-bin"

func (s *Service) reviewerKeyPath() string { return filepath.Join(s.dir, "ssh", "reviewer_ed25519") }

func (s *Service) reviewer() account {
	return account{user: ReviewerUser, key: s.reviewerKeyPath(), env: "PATH=" + reviewerBin + ":/usr/local/bin:/usr/bin:/bin"}
}

// ReviewerReady reports whether the reviewer can log in: the lab is set up and has its key
// (deploy/lab/add-reviewer.sh).
func (s *Service) ReviewerReady() bool {
	if !s.Configured() {
		return false
	}
	_, err := os.Stat(s.reviewerKeyPath())
	return err == nil
}

// errNoReviewer is what an inspection says when the reviewer account isn't set up.
var errNoReviewer = errors.New("the lab has no reviewer account yet: the owner runs deploy/lab/add-reviewer.sh on the homelab")

// Inspect runs one command on the lab as the reviewer and lays out its result, with the output
// wrapped as untrusted. A command ReadOnly turns away isn't sent, and the reason comes back as
// the result: the reviewer reads it and tries another way.
func (s *Service) Inspect(ctx context.Context, command string) (string, error) {
	if !s.ReviewerReady() {
		return "", errNoReviewer
	}
	if err := ReadOnly(command); err != nil {
		return "Not run: " + err.Error() + ". Only read-only commands work here (see the tool's description).", nil
	}
	res, err := s.run(ctx, s.reviewer(), command, nil, InspectLimit, inspectMaxOut)
	if err != nil {
		return "", err
	}
	return describe(res, InspectLimit), nil
}
