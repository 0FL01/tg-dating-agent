# TG Dating Agent

Двухкомпонентная система для автоматизации работы с `@leomatchbot`:

## Компоненты

### Dating Agent (userbot)
Запускается от вашего Telegram-аккаунта, анализирует анкеты, определяет MBTI-тип через LLM и генерирует первое сообщение через OpenRouter. При взаимном лайке отправляет уведомление через вебхук.

### Match Forwarder (HTTP webhook → Telegram bot)
Принимает вебхуки с событиями mutual-likes от Dating Agent и доставляет уведомления в указанный чат через Telegram Bot API.

## Возможности

**Dating Agent:**
- Автообработка анкет из `@leomatchbot`
- Генерация первого сообщения через LLM
- Определение MBTI-типа через мультимодальную модель (фото + текст)
- Фильтрация по MBTI (`DATING_MBTI_ALLOWLIST`)
- Кэширование результатов LLM для повторных анкет
- Обработка альбомов с детерминированным привязыванием текста
- Отправка уведомлений о взаимных лайках через вебхук
- Повторная отправка сообщений (too long/too short)
- Паузы между действиями (`DATING_ACTION_DELAY`) с jitter
- JSONL-логирование LLM-ответов (опционально)

**Match Forwarder:**
- HTTP webhook сервер для приема событий mutual-likes
- Поддержка multipart-загрузки (JSON + фотографии)
- Аутентификация через Bearer token или custom header
- Отправка уведомлений через Telegram Bot API
- Поддержка до 10 фотографий в сообщении
- Настраиваемый timeout и bind address

## ⚠️ Важное примечание

Приложение рассчитано на работу с **английской локалью Telegram** (`en`). Кнопки интерфейса, тексты сообщений и паттерны распознавания заточены под англоязычную версию клиента. Использование с другими локалями может привести к некорректной работе.

**Match Forwarder:**
- HTTP webhook сервер для приема событий mutual-likes
- Поддержка multipart-загрузки (JSON + фотографии)
- Аутентификация через Bearer token или custom header
- Отправка уведомлений через Telegram Bot API
- Поддержка фотографий в сообщении
- Настраиваемый timeout и bind address

## Требования

- Go `1.25+` (для локального запуска)
- Docker + Docker Compose (для контейнерного запуска)
- Telegram API credentials с `https://my.telegram.org`
- API key выбранного LLM endpoint (OpenRouter, OmniRoute или OpenCode Zen/Go)
- Telegram Bot API token для Match Forwarder (создайте через [@BotFather](https://t.me/botfather))

## Быстрый старт

### Dating Agent

1. Скопируйте пример окружения:

```bash
cp env.example .env
```

2. Заполните минимум:

- `TG_APP_ID`
- `TG_APP_HASH`
- `TG_PHONE_NUMBER`
- `LLM_API_KEY` (`LLM_BASE_URL` в примере указывает на OpenRouter)
- `TG_STRING_SESSION` (рекомендуется для Docker)

Для получения `TG_STRING_SESSION` выполните один раз интерактивную авторизацию:

```bash
# Первый запуск запустит диалог авторизации в терминале
go run ./cmd/dating
```

После успешной авторизации строка сессии будет выведена в логах. Добавьте её в `.env` как `TG_STRING_SESSION`.

### Match Forwarder

Для работы уведомлений о взаимных лайках заполните в `.env`:

- `DATING_MATCH_WEBHOOK_URL` — URL вашего forwarder (например: `http://localhost:8080/webhook/reciprocal-like-final`)
- `DATING_MATCH_WEBHOOK_TOKEN` — токен для аутентификации

В отдельном `.env.forwarder` для Match Forwarder:

- `FORWARDER_BOT_TOKEN` — токен бота от [@BotFather](https://t.me/botfather)
- `FORWARDER_TARGET_CHAT_ID` — ID чата для уведомлений
- `FORWARDER_WEBHOOK_AUTH_TOKEN` — токен для защиты вебхука (должен совпадать с `DATING_MATCH_WEBHOOK_TOKEN`)

## Запуск

### Dating Agent

```bash
docker compose up -d --build
docker compose logs -f dating
```

Остановка:

```bash
docker compose down
```

### Match Forwarder

Создайте `.env.forwarder` и запустите:

```bash
docker compose -f docker-compose.forwarder.yml up -d --build
docker compose -f docker-compose.forwarder.yml logs -f forwarder
```

Остановка:

```bash
docker compose -f docker-compose.forwarder.yml down
```

### Оба сервиса вместе

```bash
# Dating Agent
docker compose up -d --build dating

# Match Forwarder (в отдельном терминале или вместе)
docker compose -f docker-compose.forwarder.yml up -d --build forwarder
```

## Сессия Telegram

Поддерживаются 2 режима:

1. `TG_STRING_SESSION` — приоритетный (лучше для Docker)
2. `SESSION_PATH` — fallback для файловой сессии

В `docker-compose.yml` уже задано:

- `SESSION_PATH=/app/data/session.dat`
- volume `dating-session:/app/data`

## Основные переменные

### Dating Agent

LLM использует единый Chat Completions API для всех сервисов:

| Сервис | `LLM_BASE_URL` |
|---|---|
| OpenRouter (default) | `https://openrouter.ai/api/v1` |
| OmniRoute | `http://localhost:20128/v1` (или адрес вашего gateway) |
| OpenCode Zen | `https://opencode.ai/zen/v1` |
| OpenCode Go | `https://opencode.ai/zen/go/v1` |

Задайте `LLM_API_KEY` от выбранного endpoint и `DATING_MODEL` с его API model ID
(без CLI-префиксов `opencode/` и `opencode-go/`). Модель должна поддерживать
Chat Completions и изображения; Responses и Anthropic Messages напрямую не поддерживаются.
Base URL включает префикс API, но не `/chat/completions`.
При явном `LLM_BASE_URL` legacy-ключ не подставляется, даже для OpenRouter.
OmniRoute разворачивается отдельно; ключ gateway и upstream-маршруты настраиваются в нём.
В текущем Linux Compose с host network локальный gateway доступен через `localhost`.

[OpenCode Go](https://opencode.ai/docs/go/) ориентирован на coding agents:
до эксплуатации подтвердите допустимость dating-нагрузки. Клиент честно указывает
`User-Agent: tg-dating-agent` и отправляет непрозрачный `x-opencode-session`
на прямые Zen и Go endpoints (стабильный ID на экземпляр клиента, новый после перезапуска,
без Telegram-данных). Отдельные curl-запросы должны передавать этот заголовок самостоятельно.
Наличие этих заголовков не означает разрешение провайдера на такой сценарий.

**Обязательные:**
- `TG_APP_ID` — ID приложения с https://my.telegram.org
- `TG_APP_HASH` или `TG_APP_APP_HASH` — hash приложения
- `TG_PHONE_NUMBER` — номер телефона для авторизации
- `LLM_API_KEY` — ключ выбранного LLM endpoint; старый `OPENROUTER_API_KEY` поддерживается, только если `LLM_BASE_URL` не задан

**Опциональные:**
- `TG_STRING_SESSION` — строка сессии (приоритетно)
- `SESSION_PATH` — путь к файлу сессии (fallback, по умолчанию `session.dat`)
- `DATING_BOT_CHAT_ID` — чат id бота знакомств (по умолчанию `1234060895`)
- `LLM_BASE_URL` — OpenAI-compatible base URL без `/chat/completions`, по умолчанию `https://openrouter.ai/api/v1`
- `OPENROUTER_MODEL` — legacy-настройка; dating runtime использует `DATING_MODEL`
- `DATING_MODEL` — API model ID для MBTI и генерации (по умолчанию `google/gemini-2.5-flash-lite-preview-06-2025`)
- `DATING_TEMPERATURE` — температура генерации (по умолчанию `0.7`)
- `DATING_ACTION_DELAY` — задержка между действиями в секундах (по умолчанию `15s`)
- `DATING_JITTER_DELAY` — случайный jitter в секундах (по умолчанию `5s`)
- `DATING_SKIP_LOW_QUALITY` — пропускать короткие анкеты (по умолчанию `false`)
- `DATING_MIN_BIO_LENGTH` — минимум символов в bio (по умолчанию `50`)
- `DATING_MBTI_ALLOWLIST` — список MBTI через запятую (по умолчанию `INTJ,INFJ,ENTJ,ENFJ`)
- `DATING_PROMPT` — кастомный промпт для генерации
- `DATING_MBTI_PROMPT` — кастомный промпт для анализа MBTI
- `DATING_REPLY_AUDIT_LOG_PATH` — путь к JSONL логу ответов (пусто = отключено, по умолчанию `/app/logs/replies.jsonl`)

**Вебхук для взаимных лайков:**
- `DATING_MATCH_WEBHOOK_URL` — URL forwarder для событий mutual-like (пусто = отключено)
- `DATING_MATCH_WEBHOOK_TOKEN` — Bearer токен для аутентификации
- `DATING_MATCH_WEBHOOK_TIMEOUT` — HTTP timeout (по умолчанию `5s`)
- `DATING_INSTANCE_NAME` — идентификатор инстанса (отправляется как header `X-Dating-Instance-Name`)

### Match Forwarder

**Обязательные:**
- `FORWARDER_BOT_TOKEN` — токен бота от [@BotFather](https://t.me/botfather)
- `FORWARDER_TARGET_CHAT_ID` — ID чата для уведомлений
- `FORWARDER_WEBHOOK_AUTH_TOKEN` — токен для защиты вебхука

**Опциональные:**
- `FORWARDER_TELEGRAM_API_BASE_URL` — базовый URL Bot API (по умолчанию `https://api.telegram.org`)
- `FORWARDER_HTTP_TIMEOUT` — HTTP timeout для Bot API (по умолчанию `10s`)
- `FORWARDER_BIND_ADDRESS` — адрес для bind (по умолчанию `:8080`)
- `FORWARDER_WEBHOOK_PATH` — путь вебхука (по умолчанию `/webhook/reciprocal-like-final`)

Полный список — в `env.example`.

## Работа системы

### Поток обработки анкет

1. Получение новой анкеты из `@leomatchbot`
2. Скачивание фото(ов) анкеты
3. Анализ MBTI через LLM (мультимодально: фото + bio)
4. Фильтрация по MBTI-allowlist
5. Генерация первого сообщения через LLM
6. Отправка like с сгенерированным сообщением
7. Обработка retry-сценариев (too long/too short)
8. Кэширование результатов (1000 записей)

### Поток уведомлений о взаимных лайках

1. Dating Agent обнаруживает сообщение "start chatting" от бота
2. Извлекается контакт URL из сообщения (t.me или telegram.me)
3. Из кэша берутся данные профиля (MBTI, opener, текст)
4. Формируется payload с:
   - `event_type`: "reciprocal-like-final"
   - `contact_username`: имя пользователя
   - `deeplink_text`: ссылка t.me/username
   - `profile_text`: текст анкеты
   - `opener_text`: первое сообщение
   - `mbti`: определенный тип
5. Отправка HTTP POST на `DATING_MATCH_WEBHOOK_URL` (multipart/form-data с фотографиями)
6. Match Forwarder валидирует запрос и формирует сообщение
7. Уведомление доставляется в `FORWARDER_TARGET_CHAT_ID` через Telegram Bot API

### Управление Dating Agent

Остановить цикл обработки можно сообщением в чат с ботом:

- `*stop`
- `💤`

Отправьте эту команду как исходящее сообщение от вашего аккаунта.

## Архитектура

### Используемые паттерны

- **Worker Pool**: Очередь анкет (buffer 50) с dedicated worker goroutine
- **State Machine**: Отслеживание состояния (idle, viewing, paused, stopped)
- **Retry with Exponential Backoff**: Все внешние вызовы с jitter
- **Caching**: LLM-результаты (1000 записей), reciprocal-like context (64 записи, TTL 30 мин)
- **Bootstrap**: Standalone handler wiring для внешних entrypoints
- **Cleanup Pattern**: Deferred cleanup для временных файлов

### Конкурентность

- Worker goroutine последовательно обрабатывает анкеты
- Graceful shutdown через quitChan
- Mutex для own-profile skip context
- Детерминированное привязывание текста к фото в альбомах
- Race-safe доступ к кэшам
