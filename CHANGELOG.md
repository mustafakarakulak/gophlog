# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.1] - 2026-09-28

Security and bug fixes. The exported API is unchanged, but several fixes change
the log output; most importantly, values that used to be written in the clear
are now masked, redacted or replaced by a marker (see Security).

### Security

- `mask` tags now apply outside the payload: struct values in `WithExtra` /
  `WithExtraField`, bound extras, `slog` attributes and `IntegrationInfo`
  request/response bodies are masked by their tags, also inside maps and
  slices. They used to be written in the clear. Name-based strategies (`Mask`,
  `MaskMany`, `WithPayloadMasked`) still apply to the payload only, and a
  `logextra` tag outside a payload leaves the field in place.
- An unrecognised or empty `mask` tag value (`mask:"hide"`, `mask:""`) now
  hides the field entirely, as `hideall` does, and in a payload or extra is
  reported to `WithOnError` (in an `IntegrationInfo` body it is only hidden).
  It used to leave the value in the clear.
- Payloads honour pointer-receiver `MarshalJSON` / `MarshalText` and
  `encoding.TextMarshaler` as `encoding/json` does, so a type that hides a
  secret in its marshaler is no longer rendered field by field. A `mask` tag on
  such a type masks the rendered text.
- middleware, httpclient: a body with a `Content-Encoding` other than
  `identity` is no longer logged raw — masking cannot see into it, and a short
  gzip body is a stored block carrying the plain text. It is logged as
  `[body not logged: non-identity Content-Encoding]`, with or without masking.
- middleware, httpclient: with masking configured, a form body that fails to
  parse (`x=%zz`, `;` separators), or that parses both as a form with a masked
  field and as JSON, is logged as `[body not logged: cannot be masked]`
  instead of unmasked.
- httpclient: URL userinfo is redacted in full, username included:
  `https://user:pass@host` → `https://xxxxx:xxxxx@host` (was `user:xxxxx@`) and
  `https://<token>@host` → `https://xxxxx@host` (was logged as-is). The URL
  fragment is dropped from `http_path`, the message and the curl dump.
- httpclient: the curl dump also redacts `Private-Token`, `X-Goog-Api-Key`,
  `X-Amz-Security-Token`, `Ocp-Apim-Subscription-Key`, `Apikey`,
  `X-Csrf-Token` and `X-Xsrf-Token`.
- middleware: an inbound workflow header is dropped when it is longer than
  1000 bytes, not valid UTF-8, or contains control, line/paragraph-separator or
  bidi embedding/override/isolate characters. Other values, including `/`, `@`
  and spaces, are still accepted.

### Fixed

- A panicking writer no longer leaves the logger's mutex locked, which
  deadlocked every later log call of the logger and its children.
- A typed-nil error, or one whose `Error` method panics, no longer crashes
  `WithError` or the `slog` adapter; it is rendered as `<nil>` or
  `%!v(PANIC=Error method: …)`, as `fmt` does.
- A NaN or ±Inf value no longer reduces the entry to the minimal record: in
  `extra` and `slog` attributes it is written as `"NaN"`, `"+Inf"` or `"-Inf"`,
  and a non-finite `duration_ms` or `integration.external_duration_ms` is
  omitted. All other fields are kept.
- Level names are matched case-insensitively: `Level("error")` filters as
  `ERROR` and is written as `ERROR` (it was filtered as `INFO` and written as
  given). Unrecognised values are still written unchanged and filtered as
  `INFO`.
- `WithError` does no work when the entry's level is disabled: no type lookup,
  `Error()` call or stack capture (about 2.7× faster in
  `BenchmarkDisabledLevel`). Note: if the minimum level is lowered after
  `WithError` but before `Log`, the entry is written without its error fields.
- A value that contains itself is cut with `"[cycle: max depth exceeded]"`
  where it first repeats and, in a payload or extra, reported to `WithOnError`
  (in an `IntegrationInfo` body it is only cut). A cyclic payload used
  to be walked to the depth limit, which takes exponentially long when it
  branches back into itself, and a cyclic extra reduced the line to the minimal
  record. In extras and `IntegrationInfo` bodies this covers cycles through
  `map[string]any` / `[]any` values and through structs with `mask` tags; a
  cycle through struct types without `mask` tags still fails there as before
  (the minimal record, or `[unserializable: …]` in a body). Extras now share the payload's depth limit: `map[string]any` / `[]any`
  nesting beyond 64 levels is cut with `"[max depth exceeded]"` instead of being
  rendered in full.
- `MaskJSON` no longer overflows the stack on a map or slice that contains
  itself; it is cut with the cycle marker, and nesting beyond 10000 levels with
  `"[max depth exceeded]"`.
- slog: a group named like a map-valued attribute no longer writes into the
  caller's map, which crashed concurrent logging.
- A nil pointer `TextMarshaler` map key no longer panics; it renders as `""`.
- Payloads, `IntegrationInfo` bodies and HTTP bodies re-encoded for masking or
  `LogExtraFields` no longer HTML-escape `<`, `>` and `&`; they are written
  literally, like the rest of the log line.
- Payloads honour `json:",string"` and `encoding/json`'s embedded-field
  dominance rules (a tagged field beats an untagged one at the same depth).
- httpclient: no longer blocks on a `101 Switching Protocols` response (the
  body stays the upgraded `io.ReadWriteCloser`) or on a streaming body (SSE,
  gRPC, Connect streaming, `stream=watch`). Both packages log
  `[body not logged: streaming]`, and the middleware records 101 as the final
  status.
- middleware: a panicking handler is logged as a 500 with `panic: …` in the
  error fields, then re-panics with the same value.
- middleware, httpclient: a body re-encoded for masking or `LogExtraFields`
  keeps its numbers exact (`12345678901234567891`, `0.1000`); they used to go
  through `float64`.
- httpclient: an empty request body is no longer sent chunked.
- middleware: excluded paths get the correlation ID, workflow IDs and client IP
  in their context, as documented. httpclient: requests filtered out by
  `ExcludeURLs` / `IncludeURLs` still carry `X-Correlation-ID`.
- middleware: `Flush` commits the 200 status in the log, as it does on the
  wire; `bytes_in` of a chunked body counts the bytes actually read instead of
  the captured prefix.

### Documentation

- A new README section, "What is masked and what is not", sets out the limits
  of masking: which parts of a record `mask` tags and name-based strategies
  reach, that field names match exactly apart from case (`access_token` does
  not cover `accessToken`), that `message`, `error_message` and `stack_trace`
  are never masked, and how non-JSON, compressed and unparseable form bodies
  are logged.
- Distributed tracing: the README now states that behind the HTTP middleware
  the correlation ID outranks a `TraceExtractor`, so request records pair a
  correlation-ID `trace_id` with the OpenTelemetry `span_id`. It shows a small
  handler that passes the OpenTelemetry trace ID to the middleware as the
  correlation ID, which makes the two agree.
- The README and the package documentation no longer claim that payloads render
  exactly as `encoding/json` would: payload keys are written sorted, so struct
  fields do not keep their declaration order. The other `encoding/json` rules
  (`json` names, `json:"-"`, `omitempty`, embedded structs) still apply.
- The `CreditCard` row of the masking-strategy table showed the grouped output
  of a 16-digit card number against the table's 11-digit example input, which
  `CreditCard` actually hides entirely. The row now shows `********`, and the
  grouped form has its own example.
- The field reference marks `event` as optional: it is omitted when empty, for
  instance on a `slog` record without an `event` attribute.
- The `hideall` note in the README and the `MaskingStrategy` doc comment no
  longer say the output never reveals a secret's length: the eight-asterisk cap
  only hides the length of longer values.
- The package documentation mentions the `log/slog` adapter.
- `SlogOptions.AddSource` no longer claims the `slog.Logger` must be created
  with `AddSource`: `slog.Logger` always records the program counter the source
  is taken from.
- The `CreditCard` doc comment gave `510152 ****** 4582` as the output for
  `5101521234564582`; it is `5101 52 **** ** 4582`.
- The Go Report Card badge is gone from the README; the service shut down on
  1 July 2026. The CI status badge stays.
- `CONTRIBUTING.md` pins the golangci-lint version CI uses and describes the
  fuzz, govulncheck, API-compatibility and release steps.

### Infrastructure

Nothing in this section changes the module contents or the exported API.

- Actions moved to their current major versions and are pinned to full commit
  SHAs, with the release in a trailing comment: `actions/checkout` v7.0.1,
  `actions/setup-go` v7.0.0, `golangci/golangci-lint-action` v9.3.0 and
  `actions/upload-artifact` v7.0.1. All of them run on Node 24, which clears
  the Node 20 deprecation warnings. Every checkout sets
  `persist-credentials: false`.
- golangci-lint v2.14.0. The configuration adds the checks Go Report Card used
  to run: `misspell`, `revive` (golint's successor; its default rules except
  `unused-parameter`), `gocyclo` (above 30) and the `gofmt` formatter, which
  runs as `gofmt -s` and replaces the separate gofmt step. Findings are no
  longer capped per linter.
- The test matrix covers Go 1.23 (the `go.mod` minimum), `oldstable` and
  `stable`. `setup-go` caching is off: without a `go.sum` there is nothing to
  cache, and it only produced a warning.
- `govulncheck` runs at a pinned version (v1.8.0) instead of `@latest`.
- A fuzz job runs every `Fuzz*` target in every package for 20 seconds and
  uploads the failing inputs as an artifact.
- An API-compatibility job runs `gorelease` against the latest release tag and
  fails on an incompatible change to the exported API.
- A release workflow: pushing a `v*` tag publishes a GitHub Release whose notes
  are the matching `CHANGELOG.md` section, and fails when there is none. It can
  also be started by hand for an existing tag.
- Dependabot opens a weekly, grouped pull request for GitHub Actions updates.
  There is no `gomod` entry, since the module has no dependencies.
- Workflows default to `contents: read`; only the release job gets
  `contents: write`.

## [1.1.0] - 2026-08-25

### Changed

- **`NewCorrelationID` now returns a UUIDv7** ([RFC 9562](https://www.rfc-editor.org/rfc/rfc9562.html)
  §5.7) in canonical lowercase form — `019baa68-80eb-7b0f-b2df-8e5a9c3e22e5` —
  instead of 32 dashless hex characters. The leading 48 bits are the generation
  time in Unix milliseconds, so IDs sort lexicographically in creation order and
  the timestamp can be recovered from the ID; the remaining 74 bits still come
  from `crypto/rand`. The format matches PostgreSQL 18's built-in `uuidv7()`, so
  correlation IDs and consumer-side database keys no longer diverge. IDs minted
  within the same millisecond are unique but unordered relative to each other.

  Still no external dependencies: the UUID is assembled from `crypto/rand` and
  `time`.

  **Migration.** No exported signature changed, so this is a minor release — but
  the *generated value* changed shape, which affects anything downstream that
  assumed the old one:

  - Log queries, dashboard filters and alert rules matching `trace_id` against
    `^[0-9a-f]{32}$` (or a bare 32-character length check) stop matching. The new
    pattern is
    `^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`.
  - Columns sized for 32 characters need 36. A `uuid`-typed column now accepts
    the value directly.
  - IDs generated before upgrading keep the old form; expect both shapes in
    retained log data.

  Inbound IDs are unaffected: `IsValidCorrelationID` and `MaxCorrelationIDLen`
  are unchanged, so the middleware still adopts any valid hex, UUID or W3C
  trace-context value a caller sends, including the old 32-hex form.
- When `crypto/rand` fails (still possible on Go 1.23; reads became infallible
  in Go 1.24), `NewCorrelationID` returns a well-formed UUIDv7 with a real
  timestamp and zeroed random bits rather than the previous run of 32 zeros. The
  result passes format validation and is never the nil UUID, while the zeroed
  entropy keeps the failure recognisable.
- The HTTP middleware now excludes `/scalar` from logging by default, alongside
  the `/swagger` entry that was already there. Scalar is a common OpenAPI
  documentation UI, and its asset requests are high-volume with no audit value.
  Services that were logging those requests will stop seeing them; add an
  explicit `IncludePaths` or override `ExcludePaths` if you want them back.

### Added

- `middleware.DefaultExcludePaths` exposes the path list `Options.ExcludePaths`
  falls back to, so custom patterns can be appended instead of silently
  replacing the defaults:

  ```go
  ExcludePaths: append(middleware.DefaultExcludePaths, "/internal/*"),
  ```

  Assigning `ExcludePaths` has always replaced the default list rather than
  extending it; that behaviour is unchanged, but it is now documented and there
  is a supported way to keep the defaults.

### Fixed

- Map keys that implement `encoding.TextMarshaler` on a string kind now follow
  the toolchain: Go 1.27 resolves `MarshalText` before the string kind, earlier
  versions resolve the string kind first. Rendering stays identical to
  `encoding/json` on every supported Go version.

### Infrastructure

Nothing in this section changes the module contents or the exported API.

- golangci-lint v2.13.1 in CI, the first release that analyses Go 1.27
  packages without crashing.

## [1.0.0] - 2026-08-14

First stable release. The behaviours below were corrected before tagging,
because they are frozen for the lifetime of v1. Every breaking change is
designed to fail at compile time; nothing changes behaviour silently.

From this release on, the exported API (types, functions and JSON field names)
stays backward compatible throughout the v1 series; breaking changes only ship
in a new major version. Packages under `internal/` are excluded from that
guarantee.

### Breaking changes

- **The library was renamed to `gophlog`.** Module path
  `github.com/mustafakarakulak/go-logging` → `github.com/mustafakarakulak/gophlog`,
  package name `logging` → `gophlog`. The package name now matches the last
  element of the module path, so the import alias is no longer needed:

  ```go
  // before
  import logging "github.com/mustafakarakulak/go-logging"
  log := logging.New()

  // after
  import "github.com/mustafakarakulak/gophlog"
  log := gophlog.New()
  ```

  The `v0.0.x` versions stay where they are on the old module path; the new path
  starts at `v1.0.0`. No `/v1` suffix is required for a v1 module path.
- The `LogRequestBody` / `LogResponseBody` fields were removed from
  `middleware.Options` and `httpclient.Options` and replaced with
  `DisableRequestBody` / `DisableResponseBody`. The docs claimed body capture
  was on by default, but the zero value of a `bool` cannot express that, so the
  real default was off. A zero-valued `Options` now captures bodies.
  `middleware.NewDefault()` = `middleware.New(middleware.Options{})`.
- `trace_id` is **no longer invented** when it cannot be resolved; the field is
  omitted entirely (`omitempty`). Stamping a distinct made-up trace onto
  unrelated lines inflated `trace_id` cardinality and made traces that never
  existed look searchable. The old behaviour is available via `WithAutoTraceID()`.
- `SlogOptions{EventKey: ""}` no longer disables the mapping; it falls back to
  the default (`"event"`). Use the new `DisableEventKey` field to turn it off.
  An unrelated setting such as `SlogOptions{AddSource: true}` no longer drops
  event mapping by accident.
- `Event.Payload` changed type from `any` to `string` (in practice it was always
  a string).
- Masking is now fail-closed for short values (see below) — output changes.
- The `CreditCard` strategy now hides values shorter than 12 digits — the
  minimum length of a real card — entirely (it used to emit a partial result
  such as `12****78`, and even an 11-digit value revealed 10 of its digits).

### Security

- **Masking was fail-open on short values.** `ShowLast1("5")` → `"*5"` exposed
  the entire value and `ShowLast2("42")` → `"*2"` exposed its last character,
  while `ShowFirst*` hid everything in the same situation. A value too short for
  the strategy to hide anything is now masked in full.
- **`LogCurl` leaked credentials.** The curl dump wrote headers such as
  `Authorization`, `Cookie` and `X-Api-Key`, plus the unmasked body, in plain
  text. Credential headers are now `[REDACTED]` and the body is written masked;
  headers named in `MaskFieldStrategies` are masked too. Headers are emitted in
  sorted order so the output is deterministic.
- **Incoming `X-Correlation-ID` is validated.** This client-controlled header is
  now checked with `IsValidCorrelationID` (at most `MaxCorrelationIDLen`
  characters, `[A-Za-z0-9._:-]`); an invalid value is discarded and a fresh ID
  is generated. A client can no longer steer the `trace_id` in your audit logs.
- **The curl dump leaked masked query parameters.** The curl request line was
  rendered from the raw URL, so a query secret named in `MaskFieldStrategies`
  (e.g. `api_key`) appeared in clear text on the curl side channel while the
  log line itself masked it. The curl output now renders the same masked URL as
  the log line.
- **Basic-auth credentials in the URL leaked.** A request to
  `https://user:password@host/...` logged the password verbatim in `http_path`,
  the log message and the curl dump — even though the `Authorization` header
  net/http derives from it was already redacted. The userinfo password is now
  rendered as `user:xxxxx@` (mirroring `url.Redacted`) everywhere the URL is
  logged.
- **`extra` could carry a value the body already masked.** A field named in
  both `LogExtraFields` and `MaskFieldStrategies` was lifted into the `extra`
  object in raw form while the logged body masked it; the same held on the
  entry path for a `logextra` tag combined with `Mask`/`MaskMany` (bound or
  per-entry). Extraction now happens after masking, and `renderPayload` applies
  the merged strategies to the extra map, so a lifted field is always the
  masked one. Fields lifted by a `logextra` tag together with a `mask` tag were
  already masked and are unchanged.

### Fixed

- **Unserializable payloads disappeared silently.** A payload containing
  `NaN`/`Inf`, a `func` or a `chan` was written as `"payload":""` with no
  warning. The `payload` field now carries `[unserializable: ...]`, `error_type`
  becomes `LogSerializationError` (unless the caller attached an error of their
  own), and the error is delivered to `WithOnError`.
- **Payload rendering disagreed with `encoding/json`:** `omitempty` was ignored
  (`{"a":"","b":0}`), and a `nil` embedded pointer was written under its Go type
  name (`{"Inner":null}`). `omitempty` now follows the same rules as the
  standard library, a `nil` embedded pointer produces no field at all, and only
  untagged embedded fields are promoted (an embedded field carrying
  `json:"nested"` stays a regular nested field).
- **Map keys of named types were rendered with literal quotes.** A
  `map[K]int` key where `K` is a named string type went through the
  `json.Marshal` fallback and produced `{"\"foo\"":1}`. Keys are now resolved
  the way `encoding/json` resolves them: string kinds by value, then
  `encoding.TextMarshaler`, then integer kinds in decimal.
- **A promoted embedded field could shadow the outer struct's field.** With
  `struct{ Inner; Name string }`, the embedded `Inner.Name` overwrote the outer
  `Name` whenever the embedded field was declared first. Field dominance now
  matches `encoding/json`: the shallower field always wins regardless of
  declaration order, and a name promoted by two embedded siblings is dropped
  entirely.
- **Exported fields of unexported embedded structs were dropped.** For
  `type base struct{ ID string }; type Outer struct{ base; ... }`,
  `encoding/json` promotes `id` but the payload renderer skipped every
  unexported field, anonymous or not, and logged `{}` for the embedded part.
  All the unexported embedded shapes now match the standard library: untagged
  promotes, a named tag nests, `json:"-"` drops, pointer variants included.
- **A failing request/response body was silently truncated for the
  application.** When a body errored mid-read (client disconnect, connection
  reset), the capture helper discarded the error and handed downstream
  consumers the partial data followed by a clean EOF: handlers received
  truncated request bodies as if complete, `client.Do` returned a partial
  response body with a nil error, and a truncated outbound request could be
  sent over the wire. The restored body now replays the read data followed by
  the ORIGINAL error, and nothing is captured for the log (a partial fragment
  cannot be masked reliably).
- **A 1xx informational response broke the final status.** The middleware's
  response recorder latched on the first `WriteHeader` call, so a handler
  sending `103 Early Hints` had its real status swallowed — the client got an
  implicit `200` and the log recorded `http_status: 103`. Informational codes
  now pass through without latching.
- **`bytes_in` reported the captured prefix, not the request size.** A body
  larger than `MaxBodySize` logged `bytes_in = MaxBodySize`, and disabling
  request capture always logged `0`. The value now comes from the declared
  `Content-Length`, falling back to the captured length for chunked bodies.
- The `[body not logged: exceeds MaxBodySize]` sentinel is no longer itself
  truncated by a very small `MaxBodySize`.

### Added

- `WithOnError(func(error))`: surfaces write and serialization failures, which
  were previously entirely silent (including `w.Write` errors).
- `WithAutoTraceID()`: restores the old automatic `trace_id` generation.
- `IsValidCorrelationID` / `MaxCorrelationIDLen`: validation for correlation IDs
  arriving from untrusted sources.
- `SlogOptions.DisableEventKey`.
- A version policy (semver commitment) section in the README, and a prominent
  warning that `Fatal` does not terminate the process.

### Changed

- `MaskAll` is deprecated; it carries the same value as `HideAll`. It will not
  be removed, but new code should use `HideAll`.
- The package-level default logger is held in an `atomic.Pointer`, so a
  `gophlog.Info(...)` call no longer takes an `RWMutex`.
- `httpclient` masks the request body exactly once (the curl dump and the log
  line share the result); it used to process the body twice, once on the success
  path and once on the error path.
- The middleware and the httpclient transport now check the logger's level
  before doing any work. A request whose level is filtered out used to pay the
  full pipeline (body capture, JSON decode/encode, masking) only to have the
  entry dropped at emit — roughly 5× the time and 6× the allocations of the
  no-log path. Body capture also stops as soon as the response status selects a
  filtered-out level (e.g. minimum level `ERROR` and a 2xx response). Context
  propagation (correlation ID, workflow headers, client IP) is unaffected.
- Body processing skips the JSON decode/encode round trip entirely when both
  `MaskFieldStrategies` and `LogExtraFields` are empty; the body is logged
  as-is (capped at `MaxBodySize`). As a side effect, a pretty-printed JSON body
  is no longer re-compacted in that configuration.
- Masking now rewrites freshly-built payload/body trees in place instead of
  deep-copying every node a second time; the exported `MaskJSON` still copies,
  since callers own their input. Lower-cased strategy and extra-field lookup
  maps are precomputed once at middleware/transport construction instead of
  being rebuilt on every request, and per-entry/per-request `extra` maps are
  allocated lazily, only when something is actually lifted into them.
- Truncation docs now state explicitly that the `... [truncated]` marker is
  appended after the cap: a truncated value can exceed the configured limit by
  the marker's length; the cap applies to the retained content.
- Replaced the deprecated `reflect.PtrTo` with `reflect.PointerTo` (same
  behaviour).

### Infrastructure

Nothing in this section changes the module contents or the exported API.

- `.golangci.yml` plus a CI lint step (`errcheck`, `staticcheck`, `ineffassign`,
  `unused`, extended `govet`). `errcheck` runs without exclusions: ignoring an
  error return has to be written as `_ =` at the call site.
- `govulncheck` in CI, on push and pull request plus a weekly cron, so a newly
  published advisory is reported even when there are no commits.
- The CI matrix covers `macos-latest` and `windows-latest` alongside
  `ubuntu-latest` (Go 1.23 and stable). The race detector needs a C toolchain,
  so it runs on Linux only; the other platforms cover host/port splitting, file
  paths in stack traces and line-ending behaviour.
- `SECURITY.md`: reporting through GitHub's private advisory flow, the classes
  of behaviour that count as vulnerabilities in this library, and what is out of
  scope.

## [0.0.3] - 2026-07-06

### Added

- **Child loggers**: the `Logger.With()` fluent builder binds shared fields
  (`Category`, `LogType`, `Tenant`, `User`, `Extra`, `Mask`, …) once and returns
  a derived logger. Precedence is entry > context > bound. Children share the
  parent's core configuration such as the writer and minimum level, and a child
  can derive further children.

### Security

- **Masking is now fail-closed.** The `mask` tag was silently skipped on
  non-standard numeric types (`int32`, `uint16`, `float32`, `json.Number`, …)
  and on types implementing `json.Marshaler` (for example `time.Time` and custom
  string wrappers), so those fields were written to logs in the clear. Every
  scalar type is now masked, and a value that cannot be rendered is hidden
  entirely rather than logged in the clear.
- A masking strategy that explicitly targets a container (object or array) now
  masks ALL scalar leaves beneath it; previously only the subfields named in the
  strategy map were masked.
- Added the `DisableForwardedHeaders` middleware option: for services that do
  not sit behind a trusted proxy, the client-spoofable `X-Forwarded-For` /
  `X-Real-IP` headers are ignored and only the connection's real address is
  logged. Client IP parsing uses `net.SplitHostPort` so it also handles IPv6
  addresses correctly.
- Added fail-closed masking regression tests that lock these behaviours in.

### Performance

- Body handling in the middleware and httpclient: JSON bodies are parsed once
  and serialized once instead of twice, halving the per-request work.
- Context fields (correlation/tenant/user/IP/workflow) are held in a single
  carrier struct, so emitting a line does one lookup instead of walking ten
  separate context chains. `WithWorkflow` adds one context node instead of three.
- `json.Marshaler` detection is cached per type, so the payload walk no longer
  scans method sets at every node.
- The timestamp format is memoized per millisecond; the `json.Encoder` is pooled
  together with the buffer; the level comparison is computed once per entry.
  `MaskString(CreditCard)` became single-pass (~60% faster, 4→1 allocation).
- Requests without a query string skip the `url.Query()` parse.

### Fixed

- `truncate` and `CapBody` no longer split multi-byte UTF-8 characters.
- Documented that the `Fatal` level does not terminate the process.

## [0.0.2] - 2026-06-30

### Added

- A `log/slog` adapter: `NewSlogHandler` / `NewSlogLogger` bridge the standard
  `log/slog` API onto this library's JSON format (level mapping, group nesting,
  `error` values rendered as strings, `EventKey` and `AddSource` options).
- `Logger.SetMinLevel`: change the minimum log level at runtime, race-free.

### Changed

- Added a depth limit (`maxPayloadDepth`) to the payload reflection walk, so a
  cyclic payload is bounded instead of overflowing the stack.
- Masking now covers query string parameters and `x-www-form-urlencoded` bodies
  as well (middleware and httpclient).
- `emit` now uses a `sync.Pool`-backed buffer plus a `json.Encoder`, with HTML
  escaping turned off. Error stack traces are captured only when the relevant
  level is enabled.

### Tests

- Test coverage went from 57.6% to 87.6%, with tests added for
  `internal/httplog`, the context helpers, the builders, the options and the
  internal slog functions.

## [0.0.1] - 2026-06-29

First public release.

### Added

- A structured JSON logger (`Logger`) writing single-line, null-safe output to
  stdout.
- The fluent `Entry` builder API, with support for HTTP, integration, queue,
  job, workflow and custom metadata.
- Log levels (TRACE/DEBUG/INFO/WARN/ERROR/FATAL) and a minimum level filter.
- Eight masking strategies plus the `mask` / `logextra` struct tags.
- `context.Context`-based correlation and trace handling with a pluggable
  `TraceExtractor` (OpenTelemetry compatible).
- `net/http` server middleware (the `middleware` package): automatic
  request/response logging, path filtering, masking, client IP, query parameters
  and workflow headers.
- An `http.RoundTripper` for outbound HTTP calls (the `httpclient` package):
  masking, URL filtering, exception logging, correlation propagation and curl
  dumps.
- Kubernetes pod metadata support, either static or from the environment.

[Unreleased]: https://github.com/mustafakarakulak/gophlog/compare/v1.1.1...HEAD
[1.1.1]: https://github.com/mustafakarakulak/gophlog/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/mustafakarakulak/gophlog/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/mustafakarakulak/gophlog/compare/v0.0.3...v1.0.0
[0.0.3]: https://github.com/mustafakarakulak/gophlog/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/mustafakarakulak/gophlog/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/mustafakarakulak/gophlog/releases/tag/v0.0.1
