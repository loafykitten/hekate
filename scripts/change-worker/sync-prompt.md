You are updating one unattended change to Hekate to the latest `main`, which moved while the
change waited to merge. Nobody can answer questions. Your working directory is the change's
branch (named below), with no merge in progress. Read `CLAUDE.md` and `docs/DEVELOPMENT.md`.

1. `git fetch origin main && git merge origin/main`. It conflicts: that's why you're here.
2. Resolve each conflict so both sides' intent survives: main's changes stay as they are, and the
   change keeps doing what its PR says. Don't take the chance to change anything else.
3. Run the gates for what the merge touched until they pass:
   `cd service && gofmt -l . && go vet ./... && golangci-lint run ./... && go test -race ./...`,
   `cd app && npm ci && npm run check`, and `scripts/check-hygiene.sh`.
4. Commit the merge. Don't push, don't touch the PR, issues or other branches: the worker pushes,
   reviews and checks it after you.

Return `synced` when the merge is committed and the gates pass, or `failed` with what stopped you,
in plain words for a non-programmer.
