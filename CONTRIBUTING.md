# Contributing

## Development

```bash
go build -o gh-wt .
gh extension install .   # `gh wt` now runs your local build
```

Before committing, make sure these pass:

```bash
gofmt -l .
go vet ./...
go test ./...
staticcheck ./...   # go install honnef.co/go/tools/cmd/staticcheck@latest
```

## Pull requests

Make one pull request per change. The release notes are generated from merged pull requests, so:

- Write the title as a line of the release notes, e.g. "Delete worktrees with -d / -D"
- Label it `enhancement` (What's New) or `bug` (Bug Fixes); anything else goes under What's Changed

## Releasing

1. Tag `main` and push the tag:
   ```bash
   git tag -m "vX.Y.Z" vX.Y.Z
   git push origin vX.Y.Z
   ```
2. The release workflow ([cli/gh-extension-precompile](https://github.com/cli/gh-extension-precompile)) builds the binaries and creates the release titled `gh-wt X.Y.Z`, with notes generated from the merged pull requests (grouped by `.github/release.yml`).
3. If users need to do something after upgrading (e.g. replace the shell function), add it to the notes:
   ```bash
   gh release edit vX.Y.Z --notes-file notes.md
   ```
