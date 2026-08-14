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
gofmt -l .        # must print nothing
go vet ./...
go build ./...
go test -race ./...
golangci-lint run ./...
```

If `golangci-lint` is not installed:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

The configuration lives in [.golangci.yml](.golangci.yml). `errcheck` is
deliberately strict: if you want to ignore an error return, write `_ =` at the
call site and add a short comment explaining why.

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

## Code of conduct

Please keep the tone respectful and constructive. Questions are welcome as
issues.
