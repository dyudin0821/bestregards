# Развёртывание в Kubernetes

Манифесты лежат в [deploy/k8s/](../deploy/k8s) и собираются Kustomize (`kubectl kustomize` входит в `kubectl`).

| Путь | Что внутри |
|---|---|
| `base/` | Namespace с профилем Pod Security `restricted`, Deployment (2 реплики), Service, Gateway и HTTPRoute (Gateway API), кластер PostgreSQL (CloudNativePG), PodDisruptionBudget, NetworkPolicy, ConfigMap, CronJob `purge` и приостановленный CronJob `setup-webhook`. |
| `base/secret.example.yaml` | Образец Secret. **Не входит в сборку:** настоящий Secret создаётся вне репозитория. |
| `monitoring/` | ServiceMonitor и PrometheusRule (нужен Prometheus Operator). |
| `overlays/production/` | Пример окружения: образ, домен, класс Gateway. Копируйте и правьте его, а не `base/`. |

## Что нужно в кластере заранее

- Kubernetes 1.30 или новее (`preStop.sleep`).
- CRD Gateway API (стандартный канал, v1.2 и новее) и контроллер для вашего `gatewayClassName`: Envoy Gateway, Cilium, Istio, NGINX Gateway Fabric.
- Оператор [CloudNativePG](https://cloudnative-pg.io/docs/current/installation_upgrade/).
- CNI, который применяет NetworkPolicy (Calico, Cilium).
- По желанию: cert-manager с поддержкой Gateway API (сертификат), Prometheus Operator (`monitoring/`).

## Первый деплой

```sh
kubectl create namespace bestregards
kubectl -n bestregards create secret generic bestregards-secrets --from-env-file=prod.secrets.env   # ключи: см. base/secret.example.yaml
# Правки под окружение: overlays/production/kustomization.yaml (образ, домен, класс Gateway)
# и base/configmap.yaml (PUBLIC_URL, цены AI, ADMIN_IDS, TURNSTILE_SITE_KEY)
kubectl apply -k deploy/k8s/overlays/production
kubectl -n bestregards wait --for=condition=Ready cluster/bestregards-db --timeout=10m   # база поднимается первой
kubectl -n bestregards rollout status deploy/bestregards
kubectl -n bestregards create job --from=cronjob/bestregards-setup-webhook setup-webhook-$(date +%s)   # один раз: вебхук бота
```

Образ берите **по дайджесту** из сводки workflow `image.yml` и сначала проверьте его аттестацию ([CI и релиз](./ci.md#проверка-образа)). Подставьте дайджест в `images` оверлея вместо `newTag: main`.

Поды сервиса при первом деплое стартуют раньше, чем готова база: они не успевают подключиться за 30 секунд, завершаются, и Kubernetes перезапускает их, пока кластер не станет готов. Это ожидаемо.

Что нужно вне манифестов:
- **Cloudflare Turnstile:** создайте виджет, добавьте домен сервиса. Публичный ключ идёт в `TURNSTILE_SITE_KEY` (ConfigMap), секрет в `TURNSTILE_SECRET` (Secret); задаются только вместе.
- **Бот:** `WEBHOOK_SECRET` и https-адрес в `PUBLIC_URL`. Без них сайт и Mini App работают, но `/start` и `/admin` нет. В BotFather задайте URL Mini App `https://<домен>/app`.

## База данных (CloudNativePG)

[database.yaml](../deploy/k8s/base/database.yaml) описывает `Cluster` (`postgresql.cnpg.io/v1`) `bestregards-db`: PostgreSQL 17, два экземпляра (основной и реплика), 10 ГиБ на экземпляр, база и владелец `bestregards`. Хранилище и размер правятся там же (`storageClass` закомментирован).

Оператор создаёт Secret `bestregards-db-app`, а сервис и задачи берут `DATABASE_URL` прямо из его ключа `uri`: пароль базы не лежит ни в репозитории, ни в `bestregards-secrets`. Адрес идёт без `sslmode`, поэтому драйвер пробует TLS и откатывается на открытое соединение; внутри кластера это допустимо.

**Бэкапы не настроены:** им нужно объектное хранилище (S3 или совместимое) с вашими ключами. Данные скромные (счётчики квоты, анонимные события, id гостей): потеря сбросит дневные квоты и аналитику, но не более. Если нужны бэкапы, добавьте плагин barman-cloud и `ScheduledBackup` ([инструкция](https://cloudnative-pg.io/docs/current/backup/)).

## Вход снаружи (Gateway API)

`gateway.yaml` создаёт `Gateway` с https-слушателем (TLS завершается на нём, сертификат лежит в Secret `bestregards-tls`; с cert-manager его выпускает аннотация на Gateway) и http-слушателем только ради редиректа 301 на https. `httproute.yaml` содержит маршрут на Service с таймаутом запроса 60 с и маршрут редиректа. Класс (`envoy-gateway` по умолчанию) и домен правятся в оверлее.

- Если в кластере уже есть общий Gateway, уберите `gateway.yaml` из `base/kustomization.yaml` и направьте `parentRefs` маршрутов на него.
- Лимита размера тела в стандартном API нет: его держит сам сервис (16 КиБ), а у реализации он настраивается своей политикой.
- С `TRUST_PROXY=1` лимиты по IP берут последнюю запись `X-Forwarded-For`. Проверьте, что ваш Gateway добавляет в неё адрес клиента (Envoy Gateway делает это по умолчанию), иначе поставьте `TRUST_PROXY: "0"`.

## Что настроено и почему

- **Права.** Все поды запускаются от пользователя 65532 с read-only корневой файловой системой, без capabilities и с профилем seccomp `RuntimeDefault`. Это проверено запуском образа с теми же ограничениями: сервис, `purge`, пробы и метрики работают.
- **Пробы.** `startupProbe` на `/healthz` с запасом на миграции; `livenessProbe` на `/healthz` (не зависит от базы, чтобы сбой базы не перезапускал все поды); `readinessProbe` на `/readyz`.
- **Остановка.** `preStop` ждёт 5 секунд, сервис добивает запросы в полёте до 30 секунд, `terminationGracePeriodSeconds` равен 45.
- **Обновление.** `RollingUpdate` с `maxUnavailable: 0`, `PodDisruptionBudget` с `minAvailable: 1`, поды раскладываются по узлам. Миграции при одновременном старте реплик берут блокировку в базе, поэтому гонки нет.
- **Метрики** слушают порт 9090; Service отдаёт его только внутри кластера, NetworkPolicy пускает на него только namespace `monitoring`, токен `METRICS_TOKEN` остаётся включённым.
- **NetworkPolicy.** Наши поды закрыты по умолчанию, открыто только нужное. Сервису: входящие от Gateway (порт 8080) и Prometheus (9090); исходящие на DNS, на порт 443 во внешние адреса (приватные диапазоны и `169.254.0.0/16` исключены) и на поды PostgreSQL. `purge` ходит только в базу, `setup-webhook` только в Telegram. Поды базы запрета по умолчанию не имеют (оператору нужен доступ к API Kubernetes), но принимают входящие только от сервиса, `purge`, соседних подов и оператора.
  **Подправьте помеченные `EDIT` места:** namespace, где работает data plane вашего Gateway, и namespace Prometheus; для оператора CloudNativePG по умолчанию указан `cnpg-system`.
- **`purge`** стартует в 03:17 по Москве, не накладывается сам на себя, укладывается в 15 минут и получает только `DATABASE_URL` из секрета базы.
- **Масштаб.** Реплик столько, чтобы `реплики × DB_MAX_CONNS` (по умолчанию 10) было меньше `max_connections` базы. Автомасштабирования нет: лимиты по IP считаются в каждом поде отдельно, а узким местом будет AI-провайдер и потолок расходов, а не процессор.
- **Приватный пакет** в GHCR требует `imagePullSecrets`: заготовка закомментирована в оверлее.

## Локальная база

`compose.yaml` поднимает только базу для разработки: пароль берётся из `POSTGRES_PASSWORD` в `.env`, порт слушает `127.0.0.1`. Для боевой базы нужны `sslmode=require` и свой пароль.
