# Changelog

Bu projenin tüm önemli değişiklikleri bu dosyada belgelenir.

Format [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) standardına,
versiyonlama ise [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
kurallarına dayanır.

## [Unreleased]

## [0.0.2] - 2026-06-30

### Eklendi

- `log/slog` adaptörü: `NewSlogHandler` / `NewSlogLogger` ile standart `log/slog`
  API'si bu kütüphanenin JSON formatına köprülenir (seviye eşleme, grup nesting,
  `error` değerlerinin string'e dönüşü, `EventKey` ve `AddSource` opsiyonları).
- `Logger.SetMinLevel`: minimum log seviyesini runtime'da, race-free değiştirme.

### Değişti

- Payload reflection yürümesine derinlik sınırı (`maxPayloadDepth`) eklendi;
  döngüsel (cyclic) payload'lar artık stack overflow yerine sınırlanır.
- Maskeleme artık query string parametrelerini ve `x-www-form-urlencoded`
  gövdelerini de kapsıyor (middleware ve httpclient).
- `emit` artık `sync.Pool`'lu buffer + `json.Encoder` kullanıyor; HTML escape
  kapatıldı. Hata stack trace'i yalnızca ilgili seviye etkinse yakalanıyor.

### Test

- Test kapsamı %57.6'dan %87.6'ya çıkarıldı; `internal/httplog`, context
  yardımcıları, builder'lar, options ve slog iç fonksiyonları için testler eklendi.

## [0.0.1] - 2026-06-29

İlk genel sürüm.

### Eklendi

- Yapısal JSON logger (`Logger`) — stdout'a tek satır, null-safe çıktı.
- Fluent `Entry` builder API: HTTP, integration, queue, job, workflow ve özel
  metadata desteği.
- Log seviyeleri (TRACE/DEBUG/INFO/WARN/ERROR/FATAL) ve minimum seviye filtresi.
- 8 maskeleme stratejisi + `mask` / `logextra` struct tag desteği.
- `context.Context` tabanlı korelasyon/trace yönetimi ve pluggable
  `TraceExtractor` (OpenTelemetry uyumlu).
- `net/http` server middleware (`middleware` paketi): otomatik istek/yanıt
  loglama, path filtreleme, maskeleme, client IP, query params, workflow
  header'ları.
- Giden HTTP çağrıları için `http.RoundTripper` (`httpclient` paketi):
  maskeleme, URL filtreleme, exception loglama, correlation propagation, curl.
- Kubernetes pod metadata desteği (statik veya env'den).

[Unreleased]: https://github.com/mustafakarakulak/go-logging/compare/v0.0.2...HEAD
[0.0.2]: https://github.com/mustafakarakulak/go-logging/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/mustafakarakulak/go-logging/releases/tag/v0.0.1
