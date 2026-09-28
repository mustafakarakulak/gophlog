# gophlog

[![CI](https://github.com/mustafakarakulak/gophlog/actions/workflows/ci.yml/badge.svg)](https://github.com/mustafakarakulak/gophlog/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mustafakarakulak/gophlog.svg)](https://pkg.go.dev/github.com/mustafakarakulak/gophlog)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8.svg)](go.mod)

Enterprise-grade **structured JSON logging** for Go, built for Kubernetes,
FluentBit and OpenSearch.

## Features

- ✅ **Structured JSON logging** — clean, single-line, parseable JSON on stdout
- ✅ **Distributed tracing** — `trace_id`/`span_id` resolved from context, with a pluggable `TraceExtractor` for OpenTelemetry
- ✅ **Type-safe constants** — strongly typed log level, type and status values
- ✅ **Kubernetes ready** — stdout output for FluentBit collection, plus optional pod metadata
- ✅ **OpenSearch friendly** — a JSON shape optimised for search and analysis
- ✅ **Rich context** — HTTP, integration, queue, job and custom metadata
- ✅ **ISO-8601 timestamps** — UTC, millisecond precision
- ✅ **Null-safe** — empty fields are dropped automatically
- ✅ **Field masking** — eight **fail-closed** strategies plus the `mask` / `logextra` struct tags
- ✅ **`encoding/json`-compatible payloads** — `omitempty`, embedded structs and `json:"-"` behave identically (keys are written in sorted order)
- ✅ **HTTP middleware** — request/response logging for `net/http`
- ✅ **HTTP client transport** — an `http.RoundTripper` for outbound calls
- ✅ **log/slog adapter** — a `slog.Handler` bridge for the standard `log/slog` API
- ✅ **Dynamic log level** — `SetMinLevel` changes it at runtime, race-free
- ✅ **Child loggers** — bind shared fields once with `With()` and derive a logger
- ✅ **No silent failures** — `WithOnError` surfaces write and serialization errors

## Installation

```bash
go get github.com/mustafakarakulak/gophlog
```

```go
import "github.com/mustafakarakulak/gophlog"
```

## Quick start

```go
log := gophlog.New()

log.Info("Resource created successfully", "resource_created").
    WithPayload(map[string]any{"id": "123", "name": "example"}).
    Log()
```

Output (a single line):

```json
{"timestamp":"2026-01-11T00:15:34.123Z","level":"INFO","event":"resource_created","message":"Resource created successfully","payload":"{\"id\":\"123\",\"name\":\"example\"}"}
```

> **Note:** `payload` is written as **stringified JSON** (JSON inside a string).
> `extra`, on the other hand, is written as a real nested JSON object, so it
> stays searchable in OpenSearch.

> **Note:** `trace_id` is written only when it can actually be resolved (see
> [Distributed Tracing](#distributed-tracing)). No ID is invented.

### Package-level default logger

```go
gophlog.SetDefault(gophlog.New(gophlog.WithMinLevel(gophlog.INFO)))

gophlog.Info("Service started", "service_start").Log()
```

## Log levels

Every level has a fluent entry point:

```go
log.Trace("Detailed trace", "trace_event").WithPayload(data).Log()
log.Debug("Debug information", "debug_event").WithPayload(data).Log()
log.Info("Informational message", "info_event").WithPayload(data).Log()
log.Warn("Warning message", "warn_event").WithPayload(data).Log()
log.Error("Error message", "error_event").WithError(err).Log()
log.Fatal("Critical failure", "fatal_event").WithError(err).Log()
```

Records below the threshold set with `gophlog.WithMinLevel(...)` are not written.
Level names match case-insensitively: `gophlog.Level("error")` filters as
`ERROR` and is written as `ERROR`. Any other value is written unchanged and
filtered as `INFO`.

> ⚠️ **`Fatal` does not terminate the process.** Unlike the standard library's
> `log.Fatal`, it only writes a record at `FATAL` level; exiting is the caller's
> decision. Call `os.Exit(1)` explicitly if that is what you want:
>
> ```go
> log.Fatal("Critical failure", "fatal_event").WithError(err).Log()
> os.Exit(1)
> ```

### Catching logging failures

When the writer returns an error, or a payload cannot be serialized, the failure
is not swallowed — `WithOnError` receives it. It also hears about values the
logger had to repair (a cycle cut with a marker or an unknown `mask` tag in a
payload or extra, a NaN or ±Inf). A cycle in an `IntegrationInfo` body is only
cut with the marker, not reported:

```go
log := gophlog.New(gophlog.WithOnError(func(err error) {
    // Bump a metric or write to stderr — do not log this with this library
    // (that recurses).
    fmt.Fprintln(os.Stderr, "logging failure:", err)
}))
```

An unserializable payload also stays visible in the log line itself: the
`payload` field carries `[unserializable: ...]`, and `error_type` becomes
`LogSerializationError` unless the caller attached an error of their own.

A NaN or ±Inf in `extra` or a `slog` attribute is written as the string `"NaN"`,
`"+Inf"` or `"-Inf"`, and a non-finite `duration_ms` or
`integration.external_duration_ms` is omitted; the rest of the line is kept.
Any other value in `extra` that cannot be serialized (a channel, for example)
reduces the line to a minimal record — `timestamp`, `level`, `trace_id`,
`event` and `message` — with `error_type` set to `LogSerializationError`.
In `extra` and in `IntegrationInfo` bodies, a cycle is cut with the marker only
when it runs through `map[string]any` / `[]any` values or structs with `mask`
tags; a cycle through struct types without `mask` tags still fails to
serialize (the minimal record, or `[unserializable: ...]` in a body).

## Fluent API (Entry) methods

```go
log.Info("Message", "event_name").
    Ctx(ctx).                                    // Context (trace/correlation)
    WithPayload(obj).                            // Payload (struct tags applied)
    WithPayloadMasked(obj, strategies).          // Payload + field masking
    WithLogType(gophlog.LogTypeApp).             // Log type (app/audit/security)
    WithCategory("category_name").               // Category
    WithError(err).                              // Error (type/message/stack)
    WithTenant("tenant_id").                     // Tenant ID
    WithUser("user_id").                         // User ID
    WithClientIP("192.168.1.1").                 // Client IP
    WithSession("session_id").                   // Session ID
    WithTraceID("...").WithSpanID("...").        // Tracing override
    WithRequestID("...").                        // Request ID
    WithHTTP("GET", "/api/test").                // HTTP method + path
    WithHTTPResult("GET", "/api/test", 200, 45.5). // HTTP + status + duration
    WithStatus(200).WithDuration(45.5).          // Individual HTTP fields
    WithQueryParams(map[string]string{...}).     // Query parameters
    WithBytes(1000, 500).                        // bytes_in / bytes_out
    WithRequestBody("...").WithResponseBody("..."). // Request/response body
    WithIntegration(&gophlog.IntegrationInfo{...}).         // Integration (full)
    WithIntegrationResult("target", status, durMs, retry).  // Integration (short)
    WithQueue(&gophlog.QueueInfo{...}).                     // Queue (full)
    WithQueueMessage("queue", "msgId", retry, ack).         // Queue (short)
    WithJob(&gophlog.JobInfo{...}).                         // Job (full)
    WithJobInfo("name", "schedule", "runId").               // Job (short)
    WithWorkflow("child", "run", "parent").      // Workflow IDs
    WithExtra(map[string]any{...}).              // Extra fields (searchable)
    WithExtraField("key", value).                // A single extra field
    Mask("field", gophlog.CreditCard).           // Mask a field inside the payload
    MaskMany(map[string]gophlog.MaskingStrategy{...}).
    Log()                                        // Write the record
```

## Child loggers (`With`)

Instead of repeating the same fields on every line, bind them once with `With()`
and use the derived logger. This suits per-module or per-service context; keep
using context propagation for per-request fields.

```go
// Once per module, at start-up:
payLog := log.With().
    Category("payments").
    LogType(gophlog.LogTypeAudit).
    ExtraField("service", "billing").
    Mask("cardNumber", gophlog.CreditCard). // applies to every payload on this logger
    Logger()

// Everywhere else:
payLog.Info("Payment charged", "payment_charged").WithPayload(p).Log()
payLog.Error("Payment failed", "payment_failed").WithError(err).Log()
```

Bindable fields: `LogType`, `Category`, `Tenant`, `User`, `ClientIP`, `Session`,
`RequestID`, `Integration`, `Queue`, `Job`, `Extra`/`ExtraField` and
`Mask`/`MaskMany`.

Precedence: **a value set explicitly on the entry > a value from context > a
bound value**. A child logger shares the parent's writer, minimum level and the
rest of its core configuration, so `SetMinLevel` affects the whole family.
Children can derive further children; extending a builder after calling
`Logger()` does not affect loggers already derived from it.

## Payload masking

### 1. Through the fluent API

```go
log.Info("Resource processed", "resource_processed").
    WithPayload(map[string]any{"cardNumber": "1234567890123456", "amount": 100}).
    Mask("cardNumber", gophlog.CreditCard).
    Log()

log.Info("Record updated", "record_updated").
    WithPayload(map[string]any{"nationalId": "12345678901", "phone": "5551234567"}).
    MaskMany(map[string]gophlog.MaskingStrategy{
        "nationalId": gophlog.ShowFirst2AndLast2,
        "phone":      gophlog.ShowLast2,
    }).
    Log()
```

### 2. Through struct tags (`mask` / `logextra`)

Sensitive fields are marked with struct tags. When `WithPayload` receives a
struct (or a pointer/slice/map of one), the `mask` and `logextra` tags are
applied **automatically**:

```go
type Request struct {
    Amount   float64 `json:"amount"`
    Currency string  `json:"currency"`

    // First 6 and last 4 digits stay visible
    CardNumber string `json:"cardNumber" mask:"creditcard"`

    // Hidden entirely
    Password string `json:"password" mask:"hideall"`

    // Lifted out of the payload into the searchable `extra` object
    RefID string `json:"refId" logextra:"true"`
}

log.Info("Request processed", "request_processed").
    WithPayload(Request{ /* ... */ }).
    Log()
```

- `mask:"..."` → the field value is masked in place. On a type that renders
  itself (`MarshalJSON` / `MarshalText`) the rendered text is masked. A value
  that names no strategy — a typo such as `mask:"hide"`, or `mask:""` — hides
  the field entirely and, in a payload or extra, is reported to `WithOnError`
  (in an `IntegrationInfo` body it is only hidden).
- `logextra:"true"` → the field is **removed** from the payload and moved into
  the `extra` object under its JSON name. Outside a payload (an extra value, a
  `slog` attribute) the field stays where it is.

Apart from those two tags, payload rendering follows the `encoding/json` rules:
`json:"-"` is skipped, `omitempty` drops empty fields, the `string` option
quotes scalars, `MarshalJSON` / `MarshalText` methods (pointer receivers
included) are called where `encoding/json` would call them, untagged embedded
struct fields are promoted to the parent object under the same name-conflict
rules, and a `nil` embedded pointer produces no field at all. The differences:

- Object keys are written sorted, the way `encoding/json` orders map keys, so
  struct fields do not keep their declaration order: a struct that declares
  `zeta` before `alpha` renders as `{"alpha":"a","zeta":"z"}`.
- `<`, `>` and `&` are written as-is; `json.Marshal` escapes them.
- A value that contains itself is cut with `"[cycle: max depth exceeded]"`
  where it first repeats, and reported to `WithOnError`; `json.Marshal` fails
  instead. Nesting deeper than 64 levels is cut with `"[max depth exceeded]"`.
- The `omitzero` option is not supported: such a field is always rendered.

### 3. Masking strategies

| Strategy | Constant | Example (`12345678932`) |
|----------|----------|-------------------------|
| Hide everything | `gophlog.HideAll` | `********` |
| First 1 | `gophlog.ShowFirst1` | `1********` |
| Last 1 | `gophlog.ShowLast1` | `**********2` |
| First 2 | `gophlog.ShowFirst2` | `12********` |
| Last 2 | `gophlog.ShowLast2` | `*********32` |
| First 1 + last 1 | `gophlog.ShowFirst1AndLast1` | `1********2` |
| First 2 + last 2 | `gophlog.ShowFirst2AndLast2` | `12*******32` |
| Credit card | `gophlog.CreditCard` | `********` (fewer than 12 digits, see below) |

`CreditCard` keeps the first six digits (the BIN) and the last four of a card
number, strips spaces, dashes and underscores, and regroups the result:
`5101521234564582` and `5101 5212 3456 4582` both become `5101 52 **** ** 4582`.

`gophlog.MaskAll` is a deprecated older name carrying the same value as
`HideAll`; use `HideAll` in new code.

> **Every strategy is fail-closed.** If a value is too short for the strategy to
> hide anything, **all** of it is masked rather than left exposed — for example
> `ShowLast2("42")` → `**` and `ShowLast1("5")` → `*`. Likewise `CreditCard`
> hides values shorter than 12 digits entirely, since those cannot be real cards.

> With the hide-everything strategy (`hideall`) the masked run is capped at eight
> asterisks, so the output does not reveal the length of a longer secret; a
> value of up to eight characters gets one asterisk per character.

### 4. What is masked and what is not

Masking works on field names, not on content — a card number inside a
free-text string is not detected — and it covers specific parts of a record:

- **`mask` struct tags** apply wherever a struct value is rendered, also inside
  maps and slices: the payload, values passed to `WithExtra` / `WithExtraField`
  (on an entry or a child logger), `log/slog` attributes, and the
  `RequestBody` / `ResponseBody` of an `IntegrationInfo`.
- **Name-based strategies** — `Mask`, `MaskMany`, `WithPayloadMasked`, a child
  logger's bound `Mask` / `MaskMany`, and the `MaskFieldStrategies` option of
  the HTTP middleware and client — apply to the payload (including the fields
  that `logextra` lifts out of it) and to HTTP bodies and query parameters.
  They do not reach `WithExtra` values, `slog` attributes or the
  `integration` / `queue` / `job` objects; use `mask` tags for those.
- **Names match exactly, ignoring case, at any depth.** A strategy for
  `access_token` covers `ACCESS_TOKEN` and `Access_Token`, but not
  `accessToken` or `access-token` — list every spelling your data uses.
- **Never masked:** `message`, `error_message` and `stack_trace`. Keep secrets
  out of log messages and error strings.
- **HTTP bodies** are masked when they are JSON or
  `application/x-www-form-urlencoded`. Any other body that does not parse as
  JSON — NDJSON, XML, plain text or malformed JSON — is logged as-is (up to
  `MaxBodySize`); turn capture off with `DisableRequestBody` /
  `DisableResponseBody` for endpoints that carry secrets in such bodies.
- **Some HTTP bodies are never logged.** The middleware and the client log a
  fixed marker in their place; the body itself still reaches the handler or
  the caller untouched:
  - `[body not logged: exceeds MaxBodySize]` — larger than `MaxBodySize`.
  - `[body not logged: non-identity Content-Encoding]` — a `Content-Encoding`
    other than `identity` (`gzip`, `br`, …), whether or not masking is
    configured.
  - `[body not logged: streaming]` — Server-Sent Events (`text/event-stream`),
    gRPC (`application/grpc*`), Connect streaming (`application/connect+*`),
    any media type with `stream=watch`, and `101 Switching Protocols`
    responses, whether or not masking is configured. Reading them up front
    would block until the stream ends.
  - `[body not logged: cannot be masked]` — only when `MaskFieldStrategies` is
    set: a form body that does not parse (`x=%zz`, `;` separators), or one
    that parses both as a form with a masked field and as JSON.
- HTTP headers are not part of the log line. The client's `LogCurl` dump,
  which does include them, redacts the credential headers (see
  [Outbound HTTP calls](#outbound-http-calls-http-client)).

## HTTP server middleware

Logs every HTTP request and response automatically. Works with `net/http`,
`chi`, `gin`'s `http.Handler` adapter and anything else built on `http.Handler`.

```go
import "github.com/mustafakarakulak/gophlog/middleware"

mw := middleware.New(middleware.Options{
    Logger:          gophlog.Default(),
    MaxBodySize:     100 * 1024,          // 100 KB
    SuccessLogLevel: gophlog.INFO,        // 2xx, 3xx
    ErrorLogLevel:   gophlog.ERROR,       // 4xx, 5xx
    EventName:       "http_request",
    IncludePaths:    []string{"/api/*"},
    ExcludePaths:    append(middleware.DefaultExcludePaths, "/internal/*"),
    MaskFieldStrategies: map[string]gophlog.MaskingStrategy{
        "cardNumber": gophlog.CreditCard,
        "nationalId": gophlog.ShowFirst2AndLast2,
    },
    LogExtraFields: []string{"externalId"}, // lifts JSON fields into extra
})

mux := http.NewServeMux()
// ... handlers
http.ListenAndServe(":8080", mw(mux))
```

Captured automatically: HTTP method/path/status, duration in milliseconds,
request and response bodies, query parameters, client IP (`X-Forwarded-For` /
`X-Real-IP`), bytes in/out, the correlation ID and the workflow headers.

A handler that panics is logged as a `500` with `panic: …` in `error_message`;
the panic then continues with the same value, so `net/http` or an outer
recovery middleware handles it as before.

**Body capture is on by default**; turn it off with `DisableRequestBody` /
`DisableResponseBody`. `middleware.NewDefault()`
(= `middleware.New(middleware.Options{})`) gets you going with the defaults.

### Skipping endpoints

Paths matching `ExcludePaths` are skipped entirely — no log line, and no body
capture or masking work either. The correlation ID, workflow IDs and client IP
still go into the request context, so records and outbound calls from an
excluded handler keep their `trace_id`.

Left unset, `ExcludePaths` falls back to `middleware.DefaultExcludePaths`:

```go
[]string{"/swagger", "/scalar", "/health", "/healthz", "/healthcheck", "/metrics"}
```

Patterns are matched case-insensitively, and a pattern **without** a wildcard
matches by prefix — so `/scalar` also covers `/scalar/openapi.json`, and
`/health` covers `/healthz` and `/health/ready`. Add a trailing `/*` to state
the prefix explicitly. Note the flip side of prefix matching: `/health` would
also silence a real `/healthy-users` endpoint; use `/health/*` plus `/healthz`
if that collides with your routes.

**Assigning `ExcludePaths` replaces the defaults rather than adding to them.**
Append to keep them:

```go
ExcludePaths: append(middleware.DefaultExcludePaths, "/internal/*"),
```

`IncludePaths` is the allowlist counterpart: when non-empty, only matching paths
are logged (an empty list includes everything). The same filtering exists for
outbound calls as `ExcludeURLs` / `IncludeURLs` in the `httpclient` package.

The incoming `X-Correlation-ID` header is client-controlled, so it is validated
(at most 128 characters, `[A-Za-z0-9._:-]`); an invalid value is discarded and a
fresh ID is generated. A client cannot steer the `trace_id` in your audit logs.
The workflow headers (`X-Child-Workflow-Id`, `X-Run-Id`,
`X-Parent-Workflow-Id`) keep an open character set, since workflow IDs may
contain `/`, `@` or spaces, but a value longer than 1000 bytes, not valid UTF-8,
or containing control characters, U+2028 / U+2029 or bidi embedding, override
or isolate characters is dropped.
If you are not behind a trusted proxy, set `DisableForwardedHeaders: true` —
otherwise `X-Forwarded-For` / `X-Real-IP` can be spoofed.

## Outbound HTTP calls (HTTP client)

An `http.RoundTripper` that logs every outbound request:

```go
import "github.com/mustafakarakulak/gophlog/httpclient"

client := httpclient.NewClient(nil, httpclient.Options{
    Logger:      gophlog.Default(),
    EventName:   "external_api_request",
    IncludeURLs: []string{"https://api.example.com/v1/*"},
    ExcludeURLs: []string{"https://api.example.com/health"},
    MaskFieldStrategies: map[string]gophlog.MaskingStrategy{
        "cardNumber": gophlog.CreditCard,
        "password":   gophlog.HideAll,
    },
    LogExtraFields: []string{"refId"},
    LogCurl:        false, // true → writes an equivalent curl command per request
                          //        (os.Stderr by default; redirect it with CurlWriter
                          //        to keep the stdout JSON stream clean)
})

// The correlation ID propagates automatically from context (X-Correlation-ID).
ctx := gophlog.WithCorrelationID(context.Background(), "cid-123")
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)
```

- Successful calls are logged at `SuccessLogLevel`; 4xx/5xx responses and
  transport errors at `ErrorLogLevel`.
- Timeouts and connection errors produce a record with `http_status: 0` and the
  event name `<event>_exception`.
- Wrap an existing `*http.Client` with `httpclient.NewClient(existing, opts)`.
- Body capture is on by default; turn it off with `DisableRequestBody` /
  `DisableResponseBody`.
- Requests filtered out by `ExcludeURLs` / `IncludeURLs` are not logged, but
  still carry `X-Correlation-ID`.
- URL userinfo is redacted in full in `http_path`, the message and the curl
  dump (`https://user:pass@host` → `https://xxxxx:xxxxx@host`,
  `https://<token>@host` → `https://xxxxx@host`), and the URL fragment is
  dropped.
- In the `LogCurl` output, credential-bearing headers are replaced with
  `[REDACTED]` and the body is written masked. The command is therefore not
  runnable as-is — that is a deliberate trade. The redacted headers are
  `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`,
  `Api-Key`, `Apikey`, `X-Auth-Token`, `X-Access-Token`, `X-Session-Token`,
  `Private-Token`, `X-Goog-Api-Key`, `X-Amz-Security-Token`,
  `Ocp-Apim-Subscription-Key`, `X-Csrf-Token` and `X-Xsrf-Token`. Any other
  header named in `MaskFieldStrategies` is masked with the corresponding
  strategy.

## Distributed Tracing

`trace_id` is resolved in this order:

1. An explicit value passed to `Entry.WithTraceID(...)`
2. The correlation ID in context (`gophlog.WithCorrelationID`)
3. The function supplied through `WithTraceExtractor` (an OpenTelemetry adapter,
   for example)
4. If none of those apply, the field is **not written**

Not inventing an ID is deliberate: stamping two unrelated lines with two
different "traces" inflates `trace_id` cardinality and makes OpenSearch return
traces that never existed. The right approach is to generate one correlation ID
per request and carry it in context — which the HTTP middleware already does.
If you need the old behaviour, enable it with `gophlog.WithAutoTraceID()`.

```go
log := gophlog.New(gophlog.WithTraceExtractor(func(ctx context.Context) (traceID, spanID string) {
    span := trace.SpanFromContext(ctx)
    sc := span.SpanContext()
    if sc.HasTraceID() {
        return sc.TraceID().String(), sc.SpanID().String()
    }
    return "", ""
}))
```

The extractor runs only when the entry and the context leave `trace_id` or
`span_id` unresolved, and it fills only what is missing. Where no correlation
ID is in context — background jobs, queue consumers — it supplies both.

> ⚠️ **Behind the HTTP middleware, `trace_id` is not the OpenTelemetry trace
> ID.** The middleware puts a correlation ID into every request context (the
> validated `X-Correlation-ID` header, or a fresh UUIDv7), and a context
> correlation ID ranks above the extractor. Records logged inside a request
> therefore carry the correlation ID as `trace_id` but the OpenTelemetry
> `span_id`, a pair that does not match up in a tracing backend.

To make the two agree, hand the OpenTelemetry trace ID to the middleware as the
correlation ID: set the header in a handler that runs after the OpenTelemetry
handler (so the span exists) and before this middleware. A 32-character hex
trace ID passes `IsValidCorrelationID`, so the middleware adopts it:

```go
func otelCorrelation(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if sc := trace.SpanContextFromContext(r.Context()); sc.HasTraceID() {
            r = r.Clone(r.Context()) // handlers must not modify the original request
            r.Header.Set(gophlog.CorrelationHeader, sc.TraceID().String())
        }
        next.ServeHTTP(w, r)
    })
}

handler := otelhttp.NewHandler(otelCorrelation(mw(mux)), "server")
```

This replaces any `X-Correlation-ID` the caller sent, and the `httpclient`
transport then forwards the trace ID as `X-Correlation-ID` on outbound calls.

Context helpers: `WithCorrelationID`, `WithSpanID`, `WithRequestID`,
`WithTenantID`, `WithUserID`, `WithClientIP`, `WithSessionID`, `WithWorkflow`.

### Generated ID format

`gophlog.NewCorrelationID()` — used by the HTTP middleware, by
`EnsureCorrelationID` and by `WithAutoTraceID` — returns a **UUIDv7**
([RFC 9562](https://www.rfc-editor.org/rfc/rfc9562.html) §5.7) in canonical
lowercase form, 36 characters including the dashes:

```text
019baa68-80eb-7b0f-b2df-8e5a9c3e22e5
└─────┬─────┘ │    │
unix_ts_ms    │    variant (0b10)
(48 bits)     version 7
```

The leading 48 bits hold the generation time in Unix milliseconds, so IDs sort
lexicographically in creation order and the timestamp can be read back out of
the ID itself. The remaining 74 bits come from `crypto/rand`. IDs minted within
the same millisecond are unique but carry no order relative to each other. The
format matches PostgreSQL 18's built-in `uuidv7()`, so IDs generated here line
up with database keys generated there.

Up to v1.0.0 this function returned 32 lowercase hex characters with no dashes.
See the [CHANGELOG](CHANGELOG.md) for the migration note if you query, index or
store `trace_id` assuming that shape.

## log/slog integration

A `slog.Handler` adapter bridges code written against the standard `log/slog`
API onto this library's JSON format, so you can move existing `slog`-based code
onto this logging stack without changing it.

```go
base := gophlog.New(gophlog.WithMinLevel(gophlog.INFO))
logger := gophlog.NewSlogLogger(base, nil) // *slog.Logger
slog.SetDefault(logger)

slog.Info("user created",
    "event", "user_created", // lifted into the "event" field
    "user_id", "u-123",
    slog.Group("db", "rows", 5, "table", "users"),
)
```

Behaviour:

- **Level mapping:** slog levels expand to the six levels here (Debug→`DEBUG`,
  Info→`INFO`, Warn→`WARN`, Error→`ERROR`; `LevelError+4` and above → `FATAL`,
  below `LevelDebug` → `TRACE`).
- **Attributes:** written into the searchable `extra` object;
  `WithGroup`/`slog.Group` is preserved as a nested object.
- **Errors:** attributes holding an `error` value are written as their
  `.Error()` string rather than as `{}` (a typed-nil error as `<nil>`).
- **`event` mapping:** `EventKey` (default `"event"`) lifts one top-level
  string attribute into the `event` field; `SlogOptions{DisableEventKey: true}`
  turns that off. An empty `EventKey` falls back to the default, so
  `SlogOptions{AddSource: true}` does not disable the mapping by accident. A
  record without that attribute has no `event` field at all.
- **Source location:** `SlogOptions{AddSource: true}` adds the caller's
  `function`/`file`/`line` under `extra.source`, taken from the record's program
  counter (a record built by hand with a zero PC gets none).

The adapter passes the official `testing/slogtest` suite, except that this
format always emits a `timestamp` field (the zero-`Record.Time` rule).

## JSON output format

### A simple record

```json
{
  "timestamp": "2026-01-11T00:15:34.123Z",
  "level": "INFO",
  "event": "resource_created",
  "message": "Resource created successfully",
  "payload": "{\"id\":\"123\",\"name\":\"example\"}"
}
```

When a correlation ID is resolved from context, the line also carries
`"trace_id": "019baa68-80eb-7b0f-b2df-8e5a9c3e22e5"`.

### An integration record

```json
{
  "timestamp": "2026-01-11T00:15:34.123Z",
  "level": "INFO",
  "trace_id": "019baa68-80eb-7b0f-b2df-8e5a9c3e22e5",
  "event": "external_call_succeeded",
  "message": "External call completed successfully",
  "payload": "{\"id\":\"123\"}",
  "integration": {
    "target": "external-gateway",
    "status": "success",
    "external_duration_ms": 80.5,
    "retry_count": 0
  }
}
```

### Field reference

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | string (ISO-8601) | UTC timestamp |
| `level` | string | TRACE/DEBUG/INFO/WARN/ERROR/FATAL (any other `Level` value is written as given) |
| `log_type` | string? | app / audit / security |
| `category` | string? | Log category |
| `trace_id` | string? | Distributed tracing ID, a UUIDv7 when generated by this library (omitted when unresolved) |
| `span_id` | string? | Span ID |
| `request_id` | string? | Request ID |
| `tenant_id`, `user_id`, `client_ip`, `session_id` | string? | Identity and session data |
| `http_method`, `http_path` | string? | HTTP method / path |
| `query_params` | object? | Query parameters |
| `http_status` | number? | HTTP status code |
| `duration_ms` | number? | Duration in milliseconds (omitted when NaN or ±Inf) |
| `bytes_in`, `bytes_out` | number? | Byte counts |
| `request_body`, `response_body` | string? | Request/response body |
| `event` | string? | Event name (omitted when empty, e.g. a `slog` record without an `event` attribute) |
| `message` | string | Log message |
| `payload` | string? | Stringified JSON payload |
| `error_type`, `error_message`, `stack_trace` | string? | Error details (stack capped at 3000 chars + truncation marker) |
| `integration`, `queue`, `job` | object? | Domain context |
| `child_workflow_id`, `run_id`, `parent_workflow_id` | string? | Workflow IDs |
| `extra` | object? | Searchable extra fields |
| `kubernetes` | object? | Kubernetes metadata |

## Kubernetes and FluentBit

Because the library writes clean JSON to stdout, it drops straight into
Kubernetes log collection. To attach pod metadata:

```go
// From the POD_NAME / POD_NAMESPACE / NODE_NAME / CONTAINER_NAME env vars
log := gophlog.New(gophlog.WithKubernetesFromEnv())

// Or statically
log := gophlog.New(gophlog.WithKubernetes(&gophlog.KubernetesInfo{
    PodName: "my-pod", Namespace: "prod",
}))
```

For FluentBit, a JSON parser plus an OpenSearch output is enough — the fields
are flat, so no extra transformation is needed.

## Enums and constants

```go
// Level
gophlog.TRACE, gophlog.DEBUG, gophlog.INFO, gophlog.WARN, gophlog.ERROR, gophlog.FATAL

// LogType
gophlog.LogTypeApp, gophlog.LogTypeAudit, gophlog.LogTypeSecurity

// IntegrationStatus
gophlog.IntegrationSuccess, gophlog.IntegrationFail, gophlog.IntegrationTimeout, gophlog.IntegrationRetry

// MaskingStrategy (MaskAll = HideAll, deprecated)
gophlog.HideAll, gophlog.ShowFirst1, gophlog.ShowLast1,
gophlog.ShowFirst2, gophlog.ShowLast2, gophlog.ShowFirst1AndLast1,
gophlog.ShowFirst2AndLast2, gophlog.CreditCard
```

## Version policy

Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
From `v1.0.0` onwards the exported API (types, functions and JSON field names)
stays backward compatible throughout the v1 series; breaking changes only ship
in a new major version. Packages under `internal/` are excluded from that
guarantee.

## Security

Masking is fail-closed by design, and credential-bearing HTTP headers are
redacted in the curl dump; [what is masked and what is not](#4-what-is-masked-and-what-is-not)
lists the limits. If you find a leak or a bypassed mask, please do not
open a public issue — see [SECURITY.md](SECURITY.md) for the reporting flow and
what is in scope.

## Best practices

- **Event names**: keep them consistent and searchable — `resource_created`,
  `request_processed`, `user_login_failed` (`{entity}_{action}`).
- **Payload**: carry the business context that matters; avoid large data sets;
  always mask sensitive values.
- **Masking**: `CreditCard` for card numbers, `ShowFirst2AndLast2` for national
  IDs, `HideAll` for passwords and tokens — or do not log them at all.
- **Extra**: move the fields you want to search in OpenSearch into `extra`, via
  `logextra:"true"` or the middleware's `LogExtraFields`.
- **Tracing**: generate a correlation ID at the HTTP boundary and carry it in
  context; the transport propagates it on outbound calls automatically.

## Tests

```bash
go test ./...
go run ./examples
```

## Performance

Benchmarks for the hot paths live in `benchmark_test.go`. To measure on your own
hardware:

```bash
go test -bench . -benchmem -run '^$'
```

The numbers vary with hardware and Go version, so `allocs/op` is more meaningful
than absolute timings. A log call at a filtered-out level is designed to allocate
nothing, which makes high-volume `Debug`/`Trace` logging effectively free.

## Requirements

- Go 1.23+
- The core package depends only on the standard library (zero external
  dependencies).

## License

MIT — see [LICENSE](LICENSE).
