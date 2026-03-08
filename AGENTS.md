# Project: tg-dating-agent

Автономный Telegram userbot для автоматизации работы с `@leomatchbot`. Бот запускается от вашего Telegram-аккаунта, анализирует анкеты, определяет MBTI-тип через LLM и генерирует первое сообщение через OpenRouter.

**Tech Stack:**
- Language: Go 1.25
- Frameworks: gogram (Telegram client)
- Key Libs: go-openrouter (LLM API client)

## Branch
The default branch is `main`.

## 🏗 Project Structure

<root>/
├── cmd/
│   └── dating/
│       └── main.go              # Entry point: setup, auth, event handlers
├── internal/
│   ├── dating/
│   │   ├── dating.go            # Core logic: profile processing, message generation
│   │   ├── state.go             # State machine for conversation flow
│   │   ├── messages.go          # Constants: button texts, patterns
│   │   └── *_test.go            # Unit tests
│   ├── llm/
│   │   ├── client.go            # OpenRouter API client with multimodal support
│   │   └── types.go             # Interfaces and content types
│   ├── standalone/
│   │   ├── config.go            # Environment config loading with defaults
│   │   ├── auth.go              # Telegram auth and session management
│   │   └── bootstrap.go         # Client initialization and connection
│   ├── tghelper/
│   │   ├── retry.go             # Retry logic for Telegram API calls
│   │   └── cleanup.go           # File cleanup utilities
│   └── utils/
│       └── string.go            # String truncation and utilities
├── docker-compose.yml           # Docker Compose with persistent volume
├── Dockerfile                   # Multi-stage build, non-root user
└── env.example                  # Environment variables template

### Key Modules
- **dating**: Profile processing, MBTI filtering, message generation workflow
- **llm**: OpenRouter integration with image+text multimodal support
- **standalone**: Configuration, authentication, and bootstrap wiring
- **tghelper**: Resilient Telegram API operations with retry logic

## 🛠 Architecture & Rules

### 1. Patterns
- **State Machine**: `StateMachine` tracks conversation state (idle, viewing, paused, stopped)
- **Retry Pattern**: All external calls use `tghelper.RetryTelegram` with exponential backoff
- **Multimodal LLM**: Profiles processed with photos + text for MBTI analysis and message generation
- **Cleanup Pattern**: Deferred cleanup for temporary photo downloads

### 2. Conventions
- **Testing**: Unit tests in `*_test.go` files alongside source
- **Error Handling**: Log and continue; graceful degradation (skip profile on LLM failure)
- **Configuration**: Environment variables with sensible defaults in `internal/standalone/config.go`
- **Security**: Non-root Docker user, read-only root filesystem, tmpfs for /tmp

### 3. State Management
States: `idle` → `viewing_profiles` → `waiting_prompt` → (loop)
- Paused for 24h on daily limit
- Stopped on match or `*stop`/`💤` command
- Concurrent processing prevented via `TryStartProcessing()`

### 4. LLM Integration Flow
1. Download profile photo(s)
2. Analyze MBTI via LLM (multimodal: photo + bio)
3. Filter by MBTI allowlist (default: INTJ, INFJ, ENTJ, ENFJ)
4. Generate first message via LLM
5. Send like with generated message

### 5. Key Environment Variables
- `TG_APP_ID`, `TG_APP_HASH` - Telegram API credentials
- `TG_STRING_SESSION` - Preferred session method for Docker
- `OPENROUTER_API_KEY` - LLM API access
- `DATING_MBTI_ALLOWLIST` - MBTI filter (comma-separated)
- `DATING_ACTION_DELAY` - Anti-spam delay between actions
- `DATING_SKIP_LOW_QUALITY` - Filter short bios
