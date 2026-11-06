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

## Releasing

1. Push `main`.
2. Tag and push the tag:
   ```bash
   git tag -m "vX.Y.Z" vX.Y.Z
   git push origin vX.Y.Z
   ```
3. The release workflow ([cli/gh-extension-precompile](https://github.com/cli/gh-extension-precompile)) builds the binaries and creates the release titled `gh-wt X.Y.Z`.
4. Changes go straight to `main` without pull requests, so the generated notes only contain the changelog link. Write the notes and apply them:
   ```bash
   gh release edit vX.Y.Z --notes-file notes.md
   ```
   Use `## What's New` for features and `## What's Changed` for fixes and other changes, and keep the `**Full Changelog**` link at the end.
