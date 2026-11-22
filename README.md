# bestregards

[![Tests](https://github.com/dyudin0821/bestregards/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/dyudin0821/bestregards/actions/workflows/test.yml)
[![Lint](https://github.com/dyudin0821/bestregards/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/dyudin0821/bestregards/actions/workflows/lint.yml)
[![Security](https://github.com/dyudin0821/bestregards/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/dyudin0821/bestregards/actions/workflows/security.yml)
[![Image](https://github.com/dyudin0821/bestregards/actions/workflows/image.yml/badge.svg?branch=main)](https://github.com/dyudin0821/bestregards/actions/workflows/image.yml)

**Переводчик между человеческой речью и корпоративным языком, и обратно.**

«Давай созвонимся» превращается в «предлагаю синкнуться и выровнять ожидания», а «синергия на уровне стейкхолдеров» обратно в «вы все друг другу что-то должны». Проект сатирический: цель в шуме и «вау», а не в выручке. Платежей нет.

Работает без регистрации на сайте (анонимный гость после проверки Cloudflare Turnstile) и в Telegram как Mini App. Лендинг, сайт и бот обслуживает один сервис.

Проект вдохновлён [«С УВАЖЕНИЕМ.»](https://github.com/DIESPECTR/s-uvazheniem) — сатирическим корпоративным переводчиком на React и Express. bestregards переписан на Go с PostgreSQL и развивается как отдельный проект.

## Возможности

- Четыре режима: с HR, с коллегами, с начальством и обратный перевод «на человеческий»; три уровня «корпоративности».
- 40 готовых пар: переводятся мгновенно, бесплатно и без квоты.
- AI-перевод любого своего текста и кнопка «Другой вариант».
- Гость на сайте: 3 AI-перевода в сутки; пользователь Telegram: 5.
- Суточный потолок расходов на AI, аварийные выключатели и панель администратора в боте (`/admin`).
- Псевдонимизированные продуктовые метрики (например, доля скопированных переводов) без текстов и персональных данных в открытом виде.

## Как устроено

Один бинарник на Go: бэкенд (`net/http`), вшитый в него фронтенд и бот. Состояние в PostgreSQL.

| | |
|---|---|
| Бэкенд | Go 1.27, `net/http`, `pgx`, `goose` (миграции), `sqlc` (запросы), `slog` |
| Фронтенд | React 19, Vite 6; шрифты Golos Text и IBM Plex Mono вшиты |
| База | PostgreSQL 17 |
| AI | любой OpenAI-совместимый API; по умолчанию OpenRouter и `deepseek/deepseek-v4-flash` |
| Доставка | OCI-образ в GHCR с аттестацией провенанса; манифесты Kubernetes (Kustomize, Gateway API, CloudNativePG) |
| Наблюдаемость | JSON-логи, метрики Prometheus, трассировка OpenTelemetry |

Подробнее: [docs/architecture.md](./docs/architecture.md).

## Быстрый старт

Нужны Go 1.27+, Node.js 22+ и Docker.

```sh
docker compose up -d db
cp .env.example .env          # заполните POSTGRES_PASSWORD (и тот же пароль в DATABASE_URL), BOT_TOKEN, SESSION_SECRET, EVENTS_PEPPER
npm --prefix web install
npm --prefix web run build    # собирает фронтенд (web/src) в web/dist
set -a; . ./.env; set +a
GUESTS_WITHOUT_CHALLENGE=1 go run ./cmd/bestregards
```

Откройте http://localhost:8080/ (лендинг) и http://localhost:8080/app (приложение). Миграции применяются при старте.

> `GUESTS_WITHOUT_CHALLENGE=1` пускает гостей без проверки Turnstile и нужна **только для локальной разработки**. В продакшене скрипты смогли бы плодить гостей без ограничений.

Без `AI_API_KEY` работают только готовые пары. Для AI-переводов задайте ключ, `AI_MODEL` и цены модели (`AI_PRICE_*`): [docs/configuration.md](./docs/configuration.md#ai-провайдер).

**Фронтенд.** `npm --prefix web run dev` поднимает Vite (`http://localhost:5173/` приложение, `/landing.html` лендинг) и проксирует `/api` на `localhost:8080`.

**Telegram.** Вход через Telegram работает только внутри Telegram: сервер проверяет подпись `initData` по `BOT_TOKEN`. Откройте HTTPS-адрес сервиса (например, через туннель `cloudflared`) как Mini App своего бота: в BotFather задайте URL `https://<хост>/app`. Для кнопки `/start` и панели `/admin` задайте `WEBHOOK_SECRET`, `PUBLIC_URL` (https) и `ADMIN_IDS`, запустите сервис и выполните `bestregards setup-webhook`.

## Конфигурация

Только переменные окружения; все они описаны в [.env.example](./.env.example) и в [docs/configuration.md](./docs/configuration.md). Сервис не запустится, пока не заданы:

| Переменная | Назначение |
|---|---|
| `DATABASE_URL` | Строка подключения к PostgreSQL. |
| `BOT_TOKEN` | Токен бота из BotFather. |
| `SESSION_SECRET` | Подпись токенов сессии, не короче 32 символов. |
| `EVENTS_PEPPER` | Ключ хэша пользователя в событиях, не короче 32 символов. |

## Тесты

```sh
go test ./...                 # без базы часть тестов хранилища пропускается
TEST_DATABASE_URL='postgres://bestregards:<POSTGRES_PASSWORD>@localhost:5432/bestregards?sslmode=disable' go test -p 1 ./...
npm --prefix web test
```

Пакеты делят одну базу, поэтому с настоящей базой запускайте с `-p 1`. После изменения `internal/store/queries.sql` или файлов в `migrations/` перегенерируйте код (версия закреплена: команда выполняет код при запуске):

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

Сборка образа: `docker build -t bestregards .`.

## Развёртывание

Образ публикуется в GHCR; манифесты Kubernetes лежат в [deploy/k8s/](./deploy/k8s) (Kustomize): Deployment, Gateway API, кластер PostgreSQL на CloudNativePG, NetworkPolicy, CronJob'ы. Пошаговая инструкция, требования к кластеру и объяснение настроек: [docs/deployment.md](./docs/deployment.md).

## Документация

| | |
|---|---|
| [Конфигурация](./docs/configuration.md) | Все переменные окружения, AI-провайдер |
| [HTTP API](./docs/api.md) | Маршруты и коды ошибок |
| [Квоты и расходы](./docs/quotas-and-spending.md) | Как ограничены расходы на AI |
| [Развёртывание](./docs/deployment.md) | Kubernetes, база, вход снаружи |
| [Эксплуатация](./docs/operations.md) | Панель `/admin`, логи, метрики, алерты, трассировка |
| [CI и релиз](./docs/ci.md) | Workflow'ы, образ и его проверка, Renovate |
| [Безопасность и данные](./docs/security.md) | Модель угроз, что хранится, куки и хранилище браузера (страница `/privacy`) |
| [Архитектура](./docs/architecture.md) | Слои и ADR |
| [Метрики продукта](./docs/metrics.md) | Определения и SQL-запросы |
| [Глоссарий](./CONTEXT.md), [дизайн](./docs/design) | Термины проекта и макеты |

## Статус

Проект в разработке, к публичному запуску не готов.

- Живые запросы к настоящим AI-провайдеру, Cloudflare Turnstile и Telegram Bot API не проверялись: весь путь отработан на поддельных сервисах с теми же контрактами.
- Манифесты Kubernetes проверены валидатором, но в кластере не запускались; workflow'ы GitHub Actions ни разу не выполнялись.
- Не реализованы: бэкапы базы и автоматическое удаление данных по запросу пользователя. Страница `/privacy` есть, но юристом не проверялась.
- Сознательно не делаем: вход на сайте через Login Widget, inline-режим бота и кнопку «В чат».

## Безопасность

Не публикуйте `.env`, токены и ключи. Модель угроз и остаточные риски: [docs/security.md](./docs/security.md).
