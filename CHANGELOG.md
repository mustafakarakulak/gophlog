# Changelog

Bu projenin tüm önemli değişiklikleri bu dosyada belgelenir.

Format [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) standardına,
versiyonlama ise [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
kurallarına dayanır.

## [Unreleased]

## [0.0.3] - 2026-07-06

### Eklendi

- **Child logger**: `Logger.With()` akıcı builder'ı ile ortak alanlar
  (`Category`, `LogType`, `Tenant`, `User`, `Extra`, `Mask`, ...) bir kez
  bağlanıp türetilmiş logger olarak kullanılabiliyor. Öncelik: entry >
  context > bound. Child'lar parent'ın writer/minimum seviye gibi çekirdek
  yapılandırmasını paylaşır; child'dan child türetilebilir.

### Güvenlik

- **Maskeleme artık fail-closed.** `mask` etiketi standart dışı sayısal
  tiplerde (`int32`, `uint16`, `float32`, `json.Number`, ...) ve
  `json.Marshaler` implemente eden tiplerde (ör. `time.Time`, özel string
  sarmalayıcıları) sessizce atlanıyordu; bu alanlar loglara açık yazılıyordu.
  Artık her skaler tip maskeleniyor; render edilemeyen bir değer açık
  loglanmak yerine tamamen gizleniyor.
- Bir konteyneri (nesne/dizi) açıkça hedefleyen maskeleme stratejisi artık
  altındaki TÜM skaler yaprakları maskeler; önceden yalnızca strateji
  haritasında adı geçen alt alanlar maskeleniyordu.
- Middleware'e `DisableForwardedHeaders` opsiyonu eklendi: güvenilir bir proxy
  arkasında olmayan servislerde, istemcinin sahteleyebileceği
  `X-Forwarded-For` / `X-Real-IP` başlıkları yok sayılıp yalnızca bağlantının
  gerçek adresi loglanır. Client IP ayrıştırması IPv6 adresleriyle de doğru
  çalışacak şekilde `net.SplitHostPort` kullanıyor.
- Bu davranışları kilitleyen fail-closed maskeleme regresyon testleri eklendi.

### Performans

- Middleware ve httpclient gövde işleme: JSON gövdeler artık iki yerine bir
  kez parse edilip bir kez serialize ediliyor (istek başına iş yükü yarıya
  indi).
- Context alanları (correlation/tenant/user/IP/workflow) tek bir taşıyıcı
  struct'ta tutuluyor; emit sırasında 10 ayrı context zinciri yürüyüşü yerine
  tek lookup yapılıyor. `WithWorkflow` üç yerine tek context düğümü ekliyor.
- `json.Marshaler` tespiti tip başına önbelleğe alındı (payload yürüyüşünde
  her düğümde method-set taraması yapılmıyor).
- Zaman damgası formatı milisaniye başına memoize edildi; `json.Encoder`
  buffer ile birlikte havuzlanıyor; seviye karşılaştırması entry başına bir
  kez hesaplanıyor. `MaskString(CreditCard)` tek geçişli hale getirildi
  (~%60 daha hızlı, 4→1 allocation).
- Query string'i olmayan isteklerde `url.Query()` parse'ı atlanıyor.

### Düzeltildi

- `truncate` ve `CapBody` artık çok baytlı UTF-8 karakterleri ortadan bölmüyor.
- `Fatal` seviyesinin process'i sonlandırmadığı belgelendi.

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

[Unreleased]: https://github.com/mustafakarakulak/go-logging/compare/v0.0.3...HEAD
[0.0.3]: https://github.com/mustafakarakulak/go-logging/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/mustafakarakulak/go-logging/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/mustafakarakulak/go-logging/releases/tag/v0.0.1
