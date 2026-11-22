# Архитектура

Один Go-бинарник: бэкенд, вшитый через `go:embed` собранный фронтенд (`web/dist`), бот и фоновые команды. Состояние хранится в PostgreSQL.

```
cmd/bestregards            разбор аргументов: сервис | purge | setup-webhook
internal/app               сборка зависимостей: serve.go, bot.go, purge.go, tracing.go
  адаптеры                 store, tgauth, session, ai, bot, server, metrics, config, turnstile, telemetry
    сценарии               auth, translate, admin   (объявляют порты; events — модель события)
      правила              limits                   (квота и потолок расходов)
        сущности           user, pairs
```

Зависимости идут только внутрь. Сценарии объявляют порты, внешние пакеты их реализуют (`turnstile` реализует `auth.Challenger`, `store` — хранилища сценариев). Правило проверяет тест в [internal/arch](../internal/arch). Обоснование: [ADR 0003](./adr/0003-clean-architecture-lite.md).

## Запросы

- **`POST /api/translate`:** сначала каталог готовых пар (бесплатно), иначе AI: резерв квоты и бюджета, вызов провайдера, сверка расхода, событие. Порядок и откаты: [квоты и расходы](./quotas-and-spending.md).
- **События** пишутся асинхронно пакетом `events` без текста; при переполнении очереди событие отбрасывается и растёт `bestregards_events_dropped_total`.
- **Миграции** (`migrations/`, goose) вшиты в бинарник и применяются при старте под блокировкой базы. SQL-запросы лежат в `internal/store/queries.sql`, код генерирует sqlc.

## Решения

| ADR | Тема |
|---|---|
| [0001](./adr/0001-stateful-backend-postgresql.md) | Бэкенд с состоянием на PostgreSQL |
| [0002](./adr/0002-go-backend.md) | Go вместо Node |
| [0003](./adr/0003-clean-architecture-lite.md) | Слои и правило зависимостей |
| [0004](./adr/0004-open-site-with-guests.md) | Открытый сайт с гостями, без платежей |
| [0005](./adr/0005-cheap-paid-ai-model.md) | Дешёвая платная модель вместо бесплатной |
| [0006](./adr/0006-legal-frame-gdpr.md) | Правовая рамка: GDPR, без стран с локализацией данных (предложено) |
| [0007](./adr/0007-consent-without-cookies.md) | Согласие на хранилище браузера, без куки |
| [0008](./adr/0008-kubernetes-gateway-cnpg-oci.md) | Kubernetes, Gateway API, CloudNativePG, подписанный OCI-образ |

Формат и правила ADR: один файл на решение, короткий текст с полем `status`, необязательные разделы «Considered Options» и «Consequences». ADR пишут только для решений, которые трудно отменить, неочевидны без контекста и выбраны из реальных альтернатив. Старый ADR не переписывают: его помечают `superseded by ADR-NNNN` и пишут новый.

Термины проекта: [CONTEXT.md](../CONTEXT.md). Дизайн: [design/](./design).
