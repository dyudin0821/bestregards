# CI и релиз

GitHub Actions, workflow'ы лежат в [.github/workflows/](../.github/workflows).

| Файл | Что делает |
|---|---|
| `test.yml` | Фронтенд (`npm test`, сборка, `npm audit --omit=dev`), затем Go 1.27 и `stable` с `-race -shuffle=on` и настоящей PostgreSQL в сервис-контейнере; проверка `go mod tidy`. |
| `lint.yml` | `go vet`, `golangci-lint` (конфиг `.golangci.yml`), проверка, что `sqlc generate` не оставляет изменений в `internal/store/db`, и проверка манифестов `deploy/k8s/` (`kubectl kustomize` + `kubeconform`). |
| `security.yml` | `govulncheck`, `gosec`, CodeQL (Go и JavaScript). Запускается и раз в неделю: новые уязвимости появляются без коммитов. |
| `image.yml` | Сборка образа, сканирование Trivy, публикация в GHCR и аттестация. |
| `renovate.yaml` | Обновление зависимостей: [ниже](#обновление-зависимостей). |

## Образ

**Артефакт релиза — один OCI-образ** `ghcr.io/<владелец>/bestregards`. Отдельных бинарников нет. Образ публикуется с пушем в `main` (теги `main` и `sha-…`) и по тегу `v1.2.3` (теги `1.2.3` и `1.2`); в pull request он только собирается. Секреты не нужны: пуш идёт через `GITHUB_TOKEN`.

Вместе с образом лежат:
- OCI-метки `org.opencontainers.image.*` (источник, ревизия, версия), связывающие пакет в GHCR с репозиторием;
- аттестации BuildKit внутри индекса образа: провенанс (`mode=max`) и SBOM;
- **подписанная аттестация провенанса SLSA** от GitHub (Sigstore, ключей хранить не нужно), записанная в реестр рядом с образом как OCI-реферер.

### Проверка образа

Тот же job сразу проверяет аттестацию (`gh attestation verify`) и пишет в сводку запуска адрес образа с дайджестом. Выкатывайте **по дайджесту**, а не по тегу, и проверяйте перед выкаткой:

```sh
gh attestation verify oci://ghcr.io/<владелец>/bestregards@sha256:<дайджест> --repo <владелец>/bestregards
```

Проверка подтверждает, что образ собран workflow'ом `image.yml` этого репозитория из конкретного коммита. Если в кластере появится политика допуска образов (Kyverno, Sigstore policy-controller), она может требовать ту же аттестацию.

Локальная сборка: `docker build -t bestregards .`.

## Обновление зависимостей

Их ведёт **Renovate**, запущенный самим репозиторием ([renovate.yaml](../.github/workflows/renovate.yaml)) с настройками из [renovate.json](../renovate.json). Запуск по понедельникам в 03:17 по Москве (внутри окна «до 6 утра в понедельник» из `renovate.json`) и вручную из вкладки Actions; ручной запуск умеет `dry-run` и `debug`-лог. Версия самого Renovate закреплена в `RENOVATE_VERSION`, её обновляет он же.

Один раз создайте секрет репозитория `RENOVATE_TOKEN` (Settings → Secrets and variables → Actions): fine-grained токен на этот репозиторий с правами Contents, Pull requests, Issues и Workflows на запись и Metadata на чтение. Право Workflows нужно, чтобы Renovate мог менять файлы в `.github/workflows/`. Не используйте `GITHUB_TOKEN`: у созданных им pull request'ов не запускаются проверки. Если установлено приложение Renovate GitHub App, отключите его для этого репозитория, иначе два Renovate будут конфликтовать.

Он обновляет Go-модули (с `go mod tidy`), npm, действия GitHub (пинит их по дайджестам с комментарием о версии), дайджесты базовых образов в Dockerfile и образы в сервис-контейнерах, версии инструментов из переменных `*_VERSION` в workflow'ах (`golangci-lint`, `gosec`, `sqlc`, `kubeconform`) и команду `sqlc` в [README](../README.md). Минорные и патч-обновления идут одним PR на группу, мажорные отдельными. Версии Go, образа `golang` в Dockerfile и Node обновляются согласованно. Автослияния нет: каждый PR проходит те же проверки.

## Настройки репозитория

CI сам их не выставит (Settings на GitHub):

- **Branches → `main`:** требовать pull request и зелёные проверки `Web`, `Test (Go 1.27)`, `Test (Go stable)`, `Lint`, `Generated code`, `Kubernetes manifests`, `Vulnerability Check`, `gosec`, `CodeQL (go)`, `CodeQL (javascript-typescript)`, `Container Scan`; не давать обходить правила и администраторам.
- **Actions → General:** права `GITHUB_TOKEN` по умолчанию «только чтение»; для форков из внешних вкладов требовать одобрение запуска.
- **Packages:** после первой публикации откройте настройки пакета, проверьте его видимость и связь с репозиторием; если пакет приватный, выдайте кластеру токен чтения (`imagePullSecrets`).

## Что в CI не проверялось

Workflow'ы проверены локально (`actionlint`, `golangci-lint`, `gosec`, `kubeconform`), но ни разу не запускались на GitHub: первый прогон может потребовать правок, особенно в `image.yml` (CodeQL, Trivy, аттестация).
