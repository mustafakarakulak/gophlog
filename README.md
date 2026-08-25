# gophlog

[![CI](https://github.com/mustafakarakulak/gophlog/actions/workflows/ci.yml/badge.svg)](https://github.com/mustafakarakulak/gophlog/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mustafakarakulak/gophlog.svg)](https://pkg.go.dev/github.com/mustafakarakulak/gophlog)
[![Go Report Card](https://goreportcard.com/badge/github.com/mustafakarakulak/gophlog)](https://goreportcard.com/report/github.com/mustafakarakulak/gophlog)
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
- ✅ **`encoding/json`-compatible payloads** — `omitempty`, embedded structs and `json:"-"` behave identically
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
is not swallowed — `WithOnError` receives it:

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

- `mask:"..."` → the field value is masked in place.
- `logextra:"true"` → the field is **removed** from the payload and moved into
  the `extra` object under its JSON name.

Payload rendering matches `encoding/json` exactly: `json:"-"` is skipped,
`omitempty` drops empty fields, untagged embedded struct fields are promoted to
the parent object, and a `nil` embedded pointer produces no field at all.

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
| Credit card | `gophlog.CreditCard` | `5101 52 **** ** 4582` |

`gophlog.MaskAll` is a deprecated older name carrying the same value as
`HideAll`; use `HideAll` in new code.

> **Every strategy is fail-closed.** If a value is too short for the strategy to
> hide anything, **all** of it is masked rather than left exposed — for example
> `ShowLast2("42")` → `**` and `ShowLast1("5")` → `*`. Likewise `CreditCard`
> hides values shorter than 12 digits entirely, since those cannot be real cards.

> With the hide-everything strategy (`hideall`) the masked run is capped at eight
> asterisks, so the output does not reveal the length of the secret.

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
    ExcludePaths:    []string{"/health", "/metrics", "/swagger/*"},
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

**Body capture is on by default**; turn it off with `DisableRequestBody` /
`DisableResponseBody`. `middleware.NewDefault()`
(= `middleware.New(middleware.Options{})`) gets you going with the defaults.

The incoming `X-Correlation-ID` header is client-controlled, so it is validated
(at most 128 characters, `[A-Za-z0-9._:-]`); an invalid value is discarded and a
fresh ID is generated. A client cannot steer the `trace_id` in your audit logs.
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
- In the `LogCurl` output, credential-bearing headers (`Authorization`,
  `Cookie`, `X-Api-Key`, …) are replaced with `[REDACTED]` and the body is
  written masked. The command is therefore not runnable as-is — that is a
  deliberate trade. Any other header named in `MaskFieldStrategies` is masked
  with the corresponding strategy.

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
  `.Error()` string rather than as `{}`.
- **`event` mapping:** `EventKey` (default `"event"`) lifts one attribute into
  the `event` field; `SlogOptions{DisableEventKey: true}` turns that off. An
  empty `EventKey` falls back to the default, so `SlogOptions{AddSource: true}`
  does not disable the mapping by accident.
- **Source location:** `SlogOptions{AddSource: true}` adds the caller's
  `function`/`file`/`line` under `extra.source` (the slog logger itself must
  have been created with `AddSource`).

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
| `level` | string | TRACE/DEBUG/INFO/WARN/ERROR/FATAL |
| `log_type` | string? | app / audit / security |
| `category` | string? | Log category |
| `trace_id` | string? | Distributed tracing ID, a UUIDv7 when generated by this library (omitted when unresolved) |
| `span_id` | string? | Span ID |
| `request_id` | string? | Request ID |
| `tenant_id`, `user_id`, `client_ip`, `session_id` | string? | Identity and session data |
| `http_method`, `http_path` | string? | HTTP method / path |
| `query_params` | object? | Query parameters |
| `http_status` | number? | HTTP status code |
| `duration_ms` | number? | Duration in milliseconds |
| `bytes_in`, `bytes_out` | number? | Byte counts |
| `request_body`, `response_body` | string? | Request/response body |
| `event` | string | Event name |
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
redacted in the curl dump. If you find a leak or a bypassed mask, please do not
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
