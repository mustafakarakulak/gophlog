# Katkı Rehberi

Katkılarınız için teşekkürler! Aşağıdaki adımlar süreci kolaylaştırır.

## Geliştirme ortamı

- Go 1.23 veya üzeri gerekir.
- Çekirdek paketin dış bağımlılığı yoktur; lütfen bu durumu korumaya çalışın.

```bash
git clone https://github.com/mustafakarakulak/go-logging.git
cd go-logging
go test ./...
```

## Pull request açmadan önce

Aşağıdakilerin hepsi temiz geçmelidir (CI de bunları kontrol eder):

```bash
gofmt -l .        # çıktı boş olmalı
go vet ./...
go build ./...
go test -race ./...
```

- Yeni davranış veya hata düzeltmesi için **test ekleyin**.
- Dışa açık (exported) API değişikliklerini doc comment ve gerekiyorsa README
  ile belgeleyin.
- Anlamlı değişiklikleri `CHANGELOG.md` içindeki `[Unreleased]` bölümüne yazın.

## Commit ve PR

- Commit mesajlarını açıklayıcı tutun.
- PR açıklamasında neyi neden değiştirdiğinizi özetleyin.
- Küçük, odaklı PR'lar daha hızlı incelenir.

## Sürümleme

Proje [Semantic Versioning](https://semver.org) kullanır. `v1.0.0` öncesinde
(`v0.x`) genel API geriye dönük uyumu bozacak şekilde değişebilir.

## Davranış kuralları

Lütfen saygılı ve yapıcı bir dil kullanın. Sorularınızı issue açarak
iletebilirsiniz.
