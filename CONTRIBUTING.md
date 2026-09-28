# Contributing

Thanks for taking the time to contribute. The steps below should make the
process straightforward.

## Development environment

- Go 1.23 or newer.
- The core package has no external dependencies; please keep it that way.

```bash
git clone https://github.com/mustafakarakulak/gophlog.git
cd gophlog
go test ./...
```

## Before opening a pull request

All of the following must pass (CI checks the same things):

```bash
gofmt -s -l .     # must print nothing
go vet ./...
go build ./...
go test -race ./...
golangci-lint run ./...
```

If `golangci-lint` is not installed, install the version CI uses:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
```

The configuration lives in [.golangci.yml](.golangci.yml). `errcheck` is
deliberately strict: if you want to ignore an error return, write `_ =` at the
call site and add a short comment explaining why. `golangci-lint run` also
reports unformatted files (`gofmt -s`); `golangci-lint fmt` fixes them.

CI additionally runs every `Fuzz*` target for 20 seconds, runs `govulncheck`,
and checks the exported API against the latest release tag, failing on an
incompatible change. To run them locally (`gorelease` refuses a working tree
with uncommitted changes, so commit first):

```bash
# one target at a time: go test -list '^Fuzz' ./... lists them
go test -run '^$' -fuzz '^FuzzMaskJSON$' -fuzztime=20s -fuzzminimizetime=5s .
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
go run golang.org/x/exp/cmd/gorelease@v0.0.0-20260908205506-85c1c2202aba \
    -base="$(git describe --tags --abbrev=0 --match 'v[0-9]*')"
```

- **Add tests** for new behaviour and for bug fixes.
- Document exported API changes in doc comments, and in the README where it
  matters.
- Record meaningful changes under the `[Unreleased]` section of `CHANGELOG.md`.

## Commits and pull requests

- Write descriptive commit messages.
- Summarise what you changed and why in the pull request description.
- Small, focused pull requests get reviewed faster.

## Versioning

The project follows [Semantic Versioning](https://semver.org). From `v1.0.0`
onwards the exported API stays backward compatible within the v1 series;
breaking changes only ship in a new major version. Packages under `internal/`
are excluded from that guarantee.

To release, move the `[Unreleased]` entries in `CHANGELOG.md` under a new
`## [X.Y.Z] - YYYY-MM-DD` heading, leaving an empty `## [Unreleased]` above it,
update the comparison links at the bottom, then push a `vX.Y.Z` tag. The release workflow
publishes a GitHub Release whose notes are that version's section; it fails if
`CHANGELOG.md` at the tag has no `## [X.Y.Z]` section.

## Code of conduct

Please keep the tone respectful and constructive. Questions are welcome as
issues.
