# Метрика: доля использованных переводов

Главная метрика запуска: **какая доля переводов была скопирована**. Считаем по результатам, а не по нажатиям: на один результат приходится не больше одного события `copied` (уникальный индекс `events_copied_once_idx`).

## Что записывается

Таблица `events`, миграция `00003_events.sql`. Текстов нет, пользователь представлен ключёвым хэшем `HMAC-SHA256(EVENTS_PEPPER, telegram_id)`, который без соли не связать с Telegram-идентификатором.

| `type` | Кто записывает | Когда |
|---|---|---|
| `translate_done` | сервер для `kind=ai`, клиент для `kind=pair` | перевод показан: AI ответил или нажато «Перевести» на готовой паре (или пара скопирована без нажатия) |
| `copied` | клиент | нажато «Скопировать» (`kind` как у перевода) |
| `quota_exhausted`, `budget_exhausted` | сервер | запрос отклонён из-за дневной квоты или потолка расходов |

`source`: `miniapp` (сессия пользователя Telegram), `web` (гость сайта или запрос без сессии). `campaign` — `start_param` первого запуска (первые 64 символа), для рекламы `tt_<кампания>`; у гостей пусто. Гость в событиях — тот же ключёвой хэш, но от отрицательного номера гостя; гость, очистивший данные браузера, считается новым человеком.

## Ограничения точности

- Клиентские события (`translate_done` для пар и все `copied`) **можно подделать**: эндпоинт открыт, у него лимит 60 запросов в минуту с IP и проверка формата. Для внутренней метрики это допустимо; решения о деньгах по ним принимать нельзя.
- События пишутся асинхронно из ограниченной очереди (2048). При сбое базы или переполнении они **теряются**, число потерь пишется в лог (`events were dropped`). Метрика занижает, но не ломает продукт.
- Копирование перевода из истории не считается: у него нет события `translate_done` с тем же `result_id`.
- Анонимные события (без сессии) принимаются и идут с пустым `user_hash`: так учитываются готовые пары, которые на лендинге и в браузере работают без входа.

## Запросы

Это обычные запросы, представлений в базе нет.

Доля скопированных переводов по дням и виду:

```sql
SELECT date_trunc('day', t.at AT TIME ZONE 'Europe/Moscow')::date AS day,
       t.kind,
       count(*)                                              AS translations,
       count(c.result_id)                                    AS copied,
       round(100.0 * count(c.result_id) / count(*), 1)       AS copied_pct
FROM events t
LEFT JOIN events c ON c.result_id = t.result_id AND c.type = 'copied'
WHERE t.type = 'translate_done' AND t.at >= now() - interval '14 days'
GROUP BY 1, 2
ORDER BY 1 DESC, 2;
```

То же в разрезе рекламной кампании:

```sql
SELECT t.campaign,
       count(*)                                        AS translations,
       count(DISTINCT NULLIF(t.user_hash, ''))        AS users,
       round(100.0 * count(c.result_id) / count(*), 1) AS copied_pct
FROM events t
LEFT JOIN events c ON c.result_id = t.result_id AND c.type = 'copied'
WHERE t.type = 'translate_done' AND t.at >= now() - interval '7 days'
GROUP BY 1
ORDER BY translations DESC;
```

Упёрлись в квоту или потолок (спрос, который не удалось обслужить):

```sql
SELECT date_trunc('day', at AT TIME ZONE 'Europe/Moscow')::date AS day, type, count(*) AS refusals
FROM events
WHERE type IN ('quota_exhausted', 'budget_exhausted') AND at >= now() - interval '14 days'
GROUP BY 1, 2
ORDER BY 1 DESC, 2;
```

Гости сайта и пользователи Telegram: сколько переводов и людей в каждом канале.

```sql
SELECT source,
       count(*)                                 AS translations,
       count(DISTINCT NULLIF(user_hash, ''))    AS people
FROM events
WHERE type = 'translate_done' AND at >= now() - interval '7 days'
GROUP BY 1
ORDER BY translations DESC;
```

## Метрики Prometheus

Отдельная вещь: не продуктовые числа, а ответ на вопрос «что-то сломалось или тормозит?». Их отдаёт порт `METRICS_ADDR` (например `:9090`; пусто = выключено; не публичный), при заданном `METRICS_TOKEN` нужен заголовок `Authorization: Bearer <token>`. Метрик Go-рантайма и процесса нет, их даёт платформа.

HTTP-метрики названы общими именами, как у других сервисов: сервис различают по лейблам цели (`job`, `service`), которые добавляет Prometheus, а не по лейблу из кода. `route` — шаблон маршрута (`POST /api/translate`, `GET /api/` для неизвестных путей под `/api/`, `unmatched` для остального), поэтому значений немного.

| Метрика | Что считает |
|---|---|
| `http_requests_total{route,code}` | обслуженные запросы по маршруту и коду ответа; доля 5xx считается из неё |
| `http_request_duration_seconds{route}` | гистограмма времени ответа (корзины до 45 с: AI отвечает долго); в формате OpenMetrics несёт exemplar `trace_id` |
| `http_rate_limited_total{route}` | запросы, отклонённые ограничителем частоты по IP |
| `bestregards_ai_errors_total` | запросы перевода, не выполненные из-за сбоя AI-провайдера |
| `bestregards_events_dropped_total` | события, потерянные до записи (очередь переполнена или сбой базы) |

Доля 5xx и P99 по маршрутам:

```promql
sum by (route) (rate(http_requests_total{job="bestregards",code=~"5.."}[5m]))
  / sum by (route) (rate(http_requests_total{job="bestregards"}[5m]))

histogram_quantile(0.99, sum by (route, le) (rate(http_request_duration_seconds_bucket{job="bestregards"}[5m])))
```

Расход на AI и счётчики использования — в панели `/admin` бота и в таблице `events`, не в Prometheus. Предложенные алерты: см. раздел «Мониторинг и логи» в [README](../README.md).

## Хранение и очистка

События живут `EVENTS_RETENTION_DAYS` дней (по умолчанию 365, решение Q25). Удаляет их подкоманда `bestregards purge` (вместе со строками гостей старше 44 дней и счётчиками квоты старше 30 дней), ей нужен только `DATABASE_URL` (и по желанию `EVENTS_RETENTION_DAYS`). В Kubernetes запускайте ежедневно как CronJob с тем же образом: `command: ["/bestregards", "purge"]`.

## Приватность

`EVENTS_PEPPER` должен быть секретным и стабильным: при смене хэши прежних событий перестанут совпадать с новыми, а утечка соли позволит подбором сопоставить хэш с известным Telegram-идентификатором.
