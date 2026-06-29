# Changelog

Bu projenin tüm önemli değişiklikleri bu dosyada belgelenir.

Format [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) standardına,
versiyonlama ise [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
kurallarına dayanır.

## [Unreleased]

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

[Unreleased]: https://github.com/mustafakarakulak/go-logging/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/mustafakarakulak/go-logging/releases/tag/v0.0.1
