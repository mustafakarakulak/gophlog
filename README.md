# go-logging

[![CI](https://github.com/mustafakarakulak/go-logging/actions/workflows/ci.yml/badge.svg)](https://github.com/mustafakarakulak/go-logging/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mustafakarakulak/go-logging.svg)](https://pkg.go.dev/github.com/mustafakarakulak/go-logging)
[![Go Report Card](https://goreportcard.com/badge/github.com/mustafakarakulak/go-logging)](https://goreportcard.com/report/github.com/mustafakarakulak/go-logging)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8.svg)](go.mod)

Kubernetes, FluentBit ve OpenSearch entegrasyonu için tasarlanmış, **enterprise-grade yapısal JSON loglama** kütüphanesi. .NET `Odeal.Logging` paketinin Go'ya birebir (feature-for-feature) port edilmiş halidir.

## Özellikler

- ✅ **Structured JSON Logging** — stdout'a temiz, tek satır, parse edilebilir JSON
- ✅ **Distributed Tracing** — `trace_id`/`span_id` context'ten çözülür; OpenTelemetry için pluggable `TraceExtractor`
- ✅ **Type-Safe Sabitler** — güçlü tipli log level, type ve status değerleri
- ✅ **Kubernetes Ready** — FluentBit toplaması için stdout çıktısı + opsiyonel pod metadata
- ✅ **OpenSearch Uyumlu** — arama ve analiz için optimize JSON yapısı
- ✅ **Kapsamlı Context** — HTTP, integration, queue, job ve özel metadata desteği
- ✅ **ISO-8601 Timestamp** — UTC, milisaniye hassasiyetinde
- ✅ **Null-Safe** — boş alanlar otomatik elenir
- ✅ **Field Masking** — 8 strateji + `mask` / `logextra` struct tag'leri
- ✅ **HTTP Middleware** — `net/http` istek/yanıt loglaması
- ✅ **HTTP Client Transport** — giden çağrılar için `http.RoundTripper`

## Kurulum

```bash
go get github.com/mustafakarakulak/go-logging
```

```go
import logging "github.com/mustafakarakulak/go-logging"
```

## Hızlı Başlangıç

```go
log := logging.New()

log.Info("Resource created successfully", "resource_created").
    WithPayload(map[string]any{"id": "123", "name": "example"}).
    Log()
```

Çıktı (tek satır):

```json
{"timestamp":"2026-01-11T00:15:34.123Z","level":"INFO","trace_id":"b7f5e0b3...","event":"resource_created","message":"Resource created successfully","payload":"{\"id\":\"123\",\"name\":\"example\"}"}
```

> **Not:** `payload` alanı .NET kütüphanesindeki gibi **stringified JSON** (string içinde JSON) olarak yazılır. `extra` alanı ise gerçek nested JSON objesi olarak yazılır; bu sayede OpenSearch'te aranabilir kalır.

### Paket düzeyinde varsayılan logger

```go
logging.SetDefault(logging.New(logging.WithMinLevel(logging.INFO)))

logging.Info("Service started", "service_start").Log()
```

## Log Level'ları

Her level için fluent bir başlangıç metodu vardır:

```go
log.Trace("Detaylı iz", "trace_event").WithPayload(data).Log()
log.Debug("Debug bilgisi", "debug_event").WithPayload(data).Log()
log.Info("Bilgi mesajı", "info_event").WithPayload(data).Log()
log.Warn("Uyarı mesajı", "warn_event").WithPayload(data).Log()
log.Error("Hata mesajı", "error_event").WithError(err).Log()
log.Fatal("Kritik hata", "fatal_event").WithError(err).Log()
```

`logging.WithMinLevel(...)` ile eşiğin altındaki kayıtlar yazılmaz.

## Fluent API (Entry) Metodları

```go
log.Info("Message", "event_name").
    Ctx(ctx).                                    // Context (trace/correlation)
    WithPayload(obj).                            // Payload (struct tag'leri işlenir)
    WithPayloadMasked(obj, strategies).          // Payload + field masking
    WithLogType(logging.LogTypeApp).             // Log type (app/audit/security)
    WithCategory("category_name").               // Kategori
    WithError(err).                              // Hata (type/message/stack)
    WithTenant("tenant_id").                     // Tenant ID
    WithUser("user_id").                         // User ID
    WithClientIP("192.168.1.1").                 // Client IP
    WithSession("session_id").                   // Session ID
    WithTraceID("...").WithSpanID("...").        // Tracing override
    WithRequestID("...").                        // Request ID
    WithHTTP("GET", "/api/test").                // HTTP method + path
    WithHTTPResult("GET", "/api/test", 200, 45.5). // HTTP + status + süre
    WithStatus(200).WithDuration(45.5).          // Tekil HTTP alanları
    WithQueryParams(map[string]string{...}).     // Query parametreleri
    WithBytes(1000, 500).                        // bytes_in / bytes_out
    WithRequestBody("...").WithResponseBody("..."). // Request/Response body
    WithIntegration(&logging.IntegrationInfo{...}).         // Integration (tam)
    WithIntegrationResult("target", status, durMs, retry).  // Integration (kısa)
    WithQueue(&logging.QueueInfo{...}).                     // Queue (tam)
    WithQueueMessage("queue", "msgId", retry, ack).         // Queue (kısa)
    WithJob(&logging.JobInfo{...}).                         // Job (tam)
    WithJobInfo("name", "schedule", "runId").               // Job (kısa)
    WithWorkflow("child", "run", "parent").      // Temporal workflow id'leri
    WithExtra(map[string]any{...}).              // Extra alanlar (aranabilir)
    WithExtraField("key", value).                // Tek extra alan
    Mask("field", logging.CreditCard).           // Payload içinde field mask
    MaskMany(map[string]logging.MaskingStrategy{...}).
    Log()                                        // Kaydı yaz
```

## Payload Masking

### 1. Fluent API ile

```go
log.Info("Resource processed", "resource_processed").
    WithPayload(map[string]any{"cardNumber": "1234567890123456", "amount": 100}).
    Mask("cardNumber", logging.CreditCard).
    Log()

log.Info("Record updated", "record_updated").
    WithPayload(map[string]any{"nationalId": "12345678901", "phone": "5551234567"}).
    MaskMany(map[string]logging.MaskingStrategy{
        "nationalId": logging.ShowFirst2AndLast2,
        "phone":      logging.ShowLast2,
    }).
    Log()
```

### 2. Struct Tag ile (`mask` / `logextra`)

.NET'teki `[Masked]` ve `[LogExtra]` attribute'larının Go karşılığı struct tag'lerdir. `WithPayload` bir struct (veya pointer/slice/map) aldığında bu tag'ler **otomatik** işlenir:

```go
type Request struct {
    Amount   float64 `json:"amount"`
    Currency string  `json:"currency"`

    // İlk 6 / son 4 gösterilir
    CardNumber string `json:"cardNumber" mask:"creditcard"`

    // Tamamen maskelenir
    Password string `json:"password" mask:"hideall"`

    // payload'dan çıkarılıp aranabilir `extra` alanına taşınır
    RefID string `json:"refId" logextra:"true"`
}

log.Info("Request processed", "request_processed").
    WithPayload(Request{ /* ... */ }).
    Log()
```

- `mask:"..."` → alan değeri yerinde maskelenir.
- `logextra:"true"` → alan payload'dan **çıkarılır** ve `extra` objesine (alanın JSON adıyla) taşınır.

### 3. Masking Stratejileri

| Strateji | Sabit | Örnek (`12345678932`) |
|----------|-------|------------------------|
| Tamamını gizle | `logging.HideAll` / `MaskAll` | `********` |
| İlk 1 | `logging.ShowFirst1` | `1********` |
| Son 1 | `logging.ShowLast1` | `**********2` |
| İlk 2 | `logging.ShowFirst2` | `12********` |
| Son 2 | `logging.ShowLast2` | `*********32` |
| İlk 1 + Son 1 | `logging.ShowFirst1AndLast1` | `1********2` |
| İlk 2 + Son 2 | `logging.ShowFirst2AndLast2` | `12*******32` |
| Kredi kartı | `logging.CreditCard` | `5101 52 **** ** 4582` |

> Maskeleme davranışı .NET `MaskHelper` ile birebir aynıdır (gizlenen kısım 8 yıldızla sınırlanır dahil).

## HTTP Server Middleware

Tüm HTTP istek/yanıtlarını otomatik loglar (`net/http`, `chi`, `gin`'in `http.Handler` adaptörü vb. ile uyumlu).

```go
import "github.com/mustafakarakulak/go-logging/middleware"

mw := middleware.New(middleware.Options{
    Logger:          logging.Default(),
    LogRequestBody:  true,
    LogResponseBody: true,
    MaxBodySize:     100 * 1024,          // 100 KB
    SuccessLogLevel: logging.INFO,        // 2xx, 3xx
    ErrorLogLevel:   logging.ERROR,       // 4xx, 5xx
    EventName:       "http_request",
    IncludePaths:    []string{"/api/*"},
    ExcludePaths:    []string{"/health", "/metrics", "/swagger/*"},
    MaskFieldStrategies: map[string]logging.MaskingStrategy{
        "cardNumber": logging.CreditCard,
        "nationalId": logging.ShowFirst2AndLast2,
    },
    LogExtraFields: []string{"externalId"}, // JSON alanlarını extra'ya taşır
})

mux := http.NewServeMux()
// ... handler'lar
http.ListenAndServe(":8080", mw(mux))
```

Otomatik yakalanan bilgiler: HTTP method/path/status, süre (ms), request/response body, query parametreleri, client IP (`X-Forwarded-For` / `X-Real-IP`), bytes in/out, correlation ID ve workflow header'ları.

`middleware.NewDefault()` ile varsayılan ayarlarla (body capture açık) hızlıca kullanılabilir.

## Giden HTTP Çağrıları (HTTP Client)

Giden tüm istekleri loglayan bir `http.RoundTripper`:

```go
import "github.com/mustafakarakulak/go-logging/httpclient"

client := httpclient.NewClient(nil, httpclient.Options{
    Logger:          logging.Default(),
    LogRequestBody:  true,
    LogResponseBody: true,
    EventName:       "external_api_request",
    IncludeURLs:     []string{"https://api.example.com/v1/*"},
    ExcludeURLs:     []string{"https://api.example.com/health"},
    MaskFieldStrategies: map[string]logging.MaskingStrategy{
        "cardNumber": logging.CreditCard,
        "password":   logging.HideAll,
    },
    LogExtraFields: []string{"refId"},
    LogCurl:        false, // true → her istek için eşdeğer curl komutu yazılır (varsayılan os.Stderr;
                          //         CurlWriter ile yönlendirilebilir — stdout JSON akışı temiz kalır)
})

// Correlation ID context üzerinden otomatik propagate edilir (X-Correlation-ID).
ctx := logging.WithCorrelationID(context.Background(), "cid-123")
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)
```

- Başarılı çağrılar `SuccessLogLevel`, 4xx/5xx ve transport hataları `ErrorLogLevel` ile loglanır.
- Timeout / bağlantı hatalarında `http_status: 0` ve `<event>_exception` event adıyla log üretilir.
- Mevcut bir `*http.Client`'ı sarmak için `httpclient.NewClient(existing, opts)` kullanın.

## Distributed Tracing

`trace_id` şu sırayla çözülür:

1. `Entry.WithTraceID(...)` ile verilen açık değer
2. Context'teki correlation ID (`logging.WithCorrelationID`)
3. `WithTraceExtractor` ile verilen fonksiyon (ör. OpenTelemetry adaptörü)
4. Yeni üretilen 32 karakterlik hex ID

```go
log := logging.New(logging.WithTraceExtractor(func(ctx context.Context) (traceID, spanID string) {
    span := trace.SpanFromContext(ctx)
    sc := span.SpanContext()
    if sc.HasTraceID() {
        return sc.TraceID().String(), sc.SpanID().String()
    }
    return "", ""
}))
```

Context yardımcıları: `WithCorrelationID`, `WithSpanID`, `WithRequestID`, `WithTenantID`, `WithUserID`, `WithClientIP`, `WithSessionID`, `WithWorkflow`.

## JSON Çıktı Formatı

### Basit log

```json
{
  "timestamp": "2026-01-11T00:15:34.123Z",
  "level": "INFO",
  "trace_id": "b7f5e0b3b78b4b0fb2df8e5a9c3e22e5",
  "event": "resource_created",
  "message": "Resource created successfully",
  "payload": "{\"id\":\"123\",\"name\":\"example\"}"
}
```

### Integration log

```json
{
  "timestamp": "2026-01-11T00:15:34.123Z",
  "level": "INFO",
  "trace_id": "b7f5e0b3...",
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

### Alan açıklamaları

| Alan | Tip | Açıklama |
|------|-----|----------|
| `timestamp` | string (ISO-8601) | UTC zaman damgası |
| `level` | string | TRACE/DEBUG/INFO/WARN/ERROR/FATAL |
| `log_type` | string? | app / audit / security |
| `category` | string? | Log kategorisi |
| `trace_id` | string | Distributed tracing ID |
| `span_id` | string? | Span ID |
| `request_id` | string? | Request ID |
| `tenant_id`, `user_id`, `client_ip`, `session_id` | string? | Kimlik/oturum bilgileri |
| `http_method`, `http_path` | string? | HTTP method / path |
| `query_params` | object? | Query parametreleri |
| `http_status` | number? | HTTP status code |
| `duration_ms` | number? | Süre (ms) |
| `bytes_in`, `bytes_out` | number? | Byte sayıları |
| `request_body`, `response_body` | string? | Request/Response body |
| `event` | string | Event adı |
| `message` | string | Log mesajı |
| `payload` | string? | Stringified JSON payload |
| `error_type`, `error_message`, `stack_trace` | string? | Hata bilgileri (stack max 3000 char) |
| `integration`, `queue`, `job` | object? | Domain context |
| `child_workflow_id`, `run_id`, `parent_workflow_id` | string? | Temporal workflow id'leri |
| `extra` | object? | Aranabilir ekstra alanlar |
| `kubernetes` | object? | Kubernetes metadata |

## Kubernetes & FluentBit

Kütüphane stdout'a temiz JSON yazdığı için Kubernetes log toplama ile sorunsuz çalışır. Pod metadata'sını eklemek için:

```go
// POD_NAME / POD_NAMESPACE / NODE_NAME / CONTAINER_NAME env değişkenlerinden
log := logging.New(logging.WithKubernetesFromEnv())

// veya statik
log := logging.New(logging.WithKubernetes(&logging.KubernetesInfo{
    PodName: "my-pod", Namespace: "prod",
}))
```

FluentBit örnek konfigürasyonu için JSON parser + OpenSearch output kullanın (alanlar düz olduğu için ek dönüşüm gerekmez).

## Enum / Sabitler

```go
// Level
logging.TRACE, logging.DEBUG, logging.INFO, logging.WARN, logging.ERROR, logging.FATAL

// LogType
logging.LogTypeApp, logging.LogTypeAudit, logging.LogTypeSecurity

// IntegrationStatus
logging.IntegrationSuccess, logging.IntegrationFail, logging.IntegrationTimeout, logging.IntegrationRetry

// MaskingStrategy
logging.HideAll, logging.MaskAll, logging.ShowFirst1, logging.ShowLast1,
logging.ShowFirst2, logging.ShowLast2, logging.ShowFirst1AndLast1,
logging.ShowFirst2AndLast2, logging.CreditCard
```

## Best Practices

- **Event adları**: tutarlı ve aranabilir: `resource_created`, `request_processed`, `user_login_failed` (`{entity}_{action}`).
- **Payload**: önemli iş context'ini tutun; büyük veri setlerinden kaçının; hassas verileri mutlaka maskeleyin.
- **Masking**: kart numarası için `CreditCard`, kimlik numarası için `ShowFirst2AndLast2`, parola/token için `HideAll` (veya hiç loglamayın).
- **Extra**: OpenSearch'te aramak istediğiniz alanları `logextra:"true"` veya middleware `LogExtraFields` ile `extra`'ya taşıyın.
- **Tracing**: HTTP girişinde correlation ID üretip context ile taşıyın; giden çağrılarda transport bunu otomatik propagate eder.

## Test

```bash
go test ./...
go run ./examples
```

## Gereksinimler

- Go 1.23+
- Çekirdek paket yalnızca standart kütüphaneye bağlıdır (sıfır dış bağımlılık).

## Lisans

MIT — bkz. [LICENSE](LICENSE).
