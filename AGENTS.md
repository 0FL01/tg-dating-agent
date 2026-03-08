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
│   │   ├── dating.go            # Core logic: profile processing, message generation, retry flow
│   │   ├── state.go             # State machine for conversation flow with pause/resume
│   │   ├── messages.go          # Constants: button texts, patterns, retry prompts
│   │   ├── markup.go            # Reply markup helpers for button detection
│   │   ├── bootstrap.go         # Standalone handler wiring for external entrypoints
│   │   └── *_test.go            # Unit tests
│   ├── llm/
│   │   ├── client.go            # OpenRouter API client with multimodal support
│   │   └── types.go             # Interfaces and content types
│   ├── standalone/
│   │   ├── config.go            # Environment config loading with defaults
│   │   ├── auth.go              # Telegram auth and session management
│   │   ├── bootstrap.go         # Client initialization and connection
│   │   └── config_test.go       # Config loading tests
│   ├── tghelper/
│   │   ├── retry.go             # Telegram-specific retry wrapper
│   │   ├── retry_core.go        # Generic retry logic with exponential backoff
│   │   └── cleanup.go           # File cleanup utilities
│   └── utils/
│       ├── string.go            # String truncation and utilities
│       └── string_test.go       # String utilities tests
├── docker-compose.yml           # Docker Compose with persistent volume
├── Dockerfile                   # Multi-stage build, non-root user
└── env.example                  # Environment variables template

### Key Modules
- **dating**: Profile processing, MBTI filtering, message generation workflow, retry logic, button detection
- **llm**: OpenRouter integration with image+text multimodal support
- **standalone**: Configuration, authentication, and bootstrap wiring
- **tghelper**: Resilient Telegram API operations with retry logic, exponential backoff, jitter

## 🛠 Architecture & Rules

### 1. Patterns
- **State Machine**: `StateMachine` tracks conversation state (idle, viewing, paused, stopped)
- **Retry Pattern**: All external calls use `tghelper.RetryTelegram` with exponential backoff and jitter; message retry handles too long/too short scenarios
- **Multimodal LLM**: Profiles processed with photos + text for MBTI analysis and message generation
- **Cleanup Pattern**: Deferred cleanup for temporary photo downloads
- **Bootstrap Pattern**: `bootstrap.go` provides standalone handler wiring for external entrypoints
- **Button Detection**: `markup.go` detects reply keyboard buttons for flow recovery

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
1. Download profile photo(s) (single photo or album)
2. Analyze MBTI via LLM (multimodal: photo + bio)
3. Filter by MBTI allowlist (default: INTJ, INFJ, ENTJ, ENFJ)
4. Generate first message via LLM
5. Send like with generated message
6. Handle retry scenarios (too long/too short messages) with fallback logic

### 5. Key Environment Variables
- `TG_APP_ID`, `TG_APP_HASH` (or `TG_APP_APP_HASH`) - Telegram API credentials
- `TG_STRING_SESSION` - Preferred session method for Docker
- `SESSION_PATH` - Fallback session file path (default: session.dat)
- `OPENROUTER_API_KEY` - LLM API access
- `OPENROUTER_MODEL` - Model for LLM requests (default: google/gemini-2.5-flash)
- `DATING_BOT_CHAT_ID` - Dating bot chat ID (default: 1234060895)
- `DATING_MODEL` - Model for dating requests (default: google/gemini-2.5-flash-lite-preview-06-2025)
- `DATING_MBTI_ALLOWLIST` - MBTI filter (comma-separated, default: INTJ,INFJ,ENTJ,ENFJ)
- `DATING_ACTION_DELAY` - Anti-spam delay between actions (default: 3s)
- `DATING_SKIP_LOW_QUALITY` - Filter short bios (default: false)
- `DATING_MIN_BIO_LENGTH` - Minimum bio length for low-quality filtering (default: 50)
- `DATING_TEMPERATURE` - LLM temperature parameter (default: 0.7)
- `DATING_PROMPT` - Custom prompt for message generation
- `DATING_MBTI_PROMPT` - Custom prompt for MBTI analysis
