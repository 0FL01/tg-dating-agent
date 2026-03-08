# TG Dating Agent

Автономный Telegram userbot для работы с `@leomatchbot`.

Бот запускается от вашего Telegram-аккаунта, анализирует анкеты и генерирует первое сообщение через OpenRouter.

## Возможности

- Автообработка анкет из `@leomatchbot`
- Генерация первого сообщения через LLM
- Фильтрация по MBTI (`DATING_MBTI_ALLOWLIST`)
- Паузы между действиями (`DATING_ACTION_DELAY`)
- Поддержка Docker и Docker Compose

## Требования

- Go `1.25+` (для локального запуска)
- Docker + Docker Compose (для контейнерного запуска)
- Telegram API credentials с `https://my.telegram.org`
- OpenRouter API key с `https://openrouter.ai`

## Быстрый старт

1. Скопируйте пример окружения:

```bash
cp env.example .env
```

2. Заполните минимум:

- `TG_APP_ID`
- `TG_APP_HASH`
- `TG_PHONE_NUMBER`
- `OPENROUTER_API_KEY`
- `TG_STRING_SESSION` (рекомендуется для Docker)

Для получения `TG_STRING_SESSION` выполните один раз интерактивную авторизацию в основном репозитории `llm-tg-assistant`:

```bash
go run ./cmd/auth
```

В этом standalone-репозитории утилиты `cmd/auth` нет.

3. Запустите одним из способов ниже.

## Запуск локально

```bash
go run ./cmd/dating
```

## Запуск в Docker

```bash
docker compose up -d --build
docker compose logs -f
```

Остановка:

```bash
docker compose down
```

## Сессия Telegram

Поддерживаются 2 режима:

1. `TG_STRING_SESSION` — приоритетный (лучше для Docker)
2. `SESSION_PATH` — fallback для файловой сессии

В `docker-compose.yml` уже задано:

- `SESSION_PATH=/app/data/session.dat`
- volume `dating-session:/app/data`

## Основные переменные

- `DATING_BOT_CHAT_ID` — чат id бота знакомств (по умолчанию `1234060895`)
- `OPENROUTER_MODEL` — базовая модель OpenRouter
- `DATING_MODEL` — модель для генерации
- `DATING_TEMPERATURE` — температура генерации
- `DATING_ACTION_DELAY` — задержка между действиями
- `DATING_SKIP_LOW_QUALITY` — пропуск слабых анкет
- `DATING_MIN_BIO_LENGTH` — минимум символов в bio
- `DATING_MBTI_ALLOWLIST` — список MBTI через запятую

Полный список — в `env.example`.

## Управление

Остановить цикл можно сообщением:

- `*stop`
- `💤`

Отправьте эту команду как исходящее сообщение от вашего аккаунта.

## Безопасность

- Контейнер запускается не от root-пользователя
- Корневая ФС контейнера read-only
- Для `/tmp` используется tmpfs
- Секреты храните только в `.env` (не коммитьте)
