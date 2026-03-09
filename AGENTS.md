# Project: tg-dating-agent

Автономный Telegram userbot для автоматизации работы с `@leomatchbot`. Бот запускается от вашего Telegram-аккаунта, анализирует анкеты, определяет MBTI-тип через LLM и генерирует первое сообщение через OpenRouter.

**Tech Stack:**
- Language: Go 1.25
- Frameworks: gogram (Telegram client)
- Key Libs: go-openrouter (LLM API client)
- Patterns: Worker Pool (profile queue), State Machine, Retry with exponential backoff

## Branch
The default branch is `main`.

## 🏗 Project Structure

<root>/
├── cmd/
│   └── dating/
│       ├── main.go              # Entry point: setup, auth, event handlers
│       └── main_test.go         # Tests for main orchestration logic
├── internal/
│   ├── dating/
│   │   ├── dating.go            # Core logic: worker pool, profile processing, message generation, retry flow
│   │   ├── state.go             # State machine with profile queue (buffer 50), worker goroutine, graceful shutdown
│   │   ├── messages.go          # Constants: button texts, patterns, retry prompts
│   │   ├── markup.go            # Reply markup helpers for button detection
│   │   ├── bootstrap.go         # Standalone handler wiring for external entrypoints
│   │   ├── dating_test.go       # Tests for dating logic
│   │   ├── state_test.go        # Tests for state machine
│   │   ├── markup_test.go       # Tests for markup helpers
│   │   └── bootstrap_test.go    # Tests for bootstrap logic
│   ├── llm/
│   │   ├── client.go            # OpenRouter API client with multimodal support
│   │   ├── types.go             # Interfaces and content types
│   │   └── client_test.go       # Tests for LLM client
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
- **dating**: Profile queue (buffer 50), worker goroutine, MBTI filtering, message generation workflow, retry logic, button detection; own-profile skip via correlated context (message-id + TTL) and startup fallback (90s TTL); recovery jobs (menu_recovery, stuck_recovery) with deduplication; deterministic album text binding (photo caption preference, message ID ordering)
- **llm**: OpenRouter integration with image+text multimodal support
- **standalone**: Configuration, authentication, and bootstrap wiring
- **tghelper**: Resilient Telegram API operations with retry logic, exponential backoff, jitter

## 🛠 Architecture & Rules

### 1. Patterns
- **Worker Pool Pattern**: Profile queue (chan ProfileJob, buffer 50) with dedicated worker goroutine for sequential processing; handlers enqueue jobs; graceful shutdown via quitChan
- **State Machine**: `StateMachine` tracks conversation state (idle, viewing, paused, stopped); owns own-profile skip context under mutex with correlation by message ID window and TTL (45s TTL, max gap 3 messages), startup fallback (90s TTL), grouped caption context (2min TTL)
- **Retry Pattern**: All external calls use `tghelper.RetryTelegram` with exponential backoff and jitter; message retry handles too long/too short scenarios
- **Multimodal LLM**: Profiles processed with photos + text for MBTI analysis and message generation
- **Cleanup Pattern**: Deferred cleanup for temporary photo downloads
- **Bootstrap Pattern**: `bootstrap.go` provides standalone handler wiring for external entrypoints
- **Button Detection**: `markup.go` detects reply keyboard buttons for flow recovery

### 2. Conventions
- **Testing**: Unit tests in `*_test.go` files alongside source; race-focused tests in dating_test.go and state_test.go cover skip-context correlation, album text binding determinism, concurrent bot peer cache access, and bootstrap sequencing with state mutations
- **Error Handling**: Log and continue; graceful degradation (skip profile on LLM failure)
- **Configuration**: Environment variables with sensible defaults in `internal/standalone/config.go`
- **Security**: Non-root Docker user, read-only root filesystem, tmpfs for /tmp

### 3. State Management
States: `idle` → `enqueue` (add to queue) → `viewing_profiles` → `waiting_prompt` → (loop)
- Paused for 24h on daily limit
- Stopped on match or `*stop`/`💤` command
- Sequential processing via worker goroutine with buffered profile queue (50 jobs)
- Graceful shutdown: worker goroutine terminated via quitChan; queue drained on stop

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
- `DATING_ACTION_DELAY` - Anti-spam delay between actions (default: 15s)
- `DATING_JITTER_DELAY` - Random jitter added to action delay (default: 5s)
- `DATING_SKIP_LOW_QUALITY` - Filter short bios (default: false)
- `DATING_MIN_BIO_LENGTH` - Minimum bio length for low-quality filtering (default: 50)
- `DATING_REPLY_AUDIT_LOG_PATH` - Reply audit JSONL path (default: /app/logs/replies.jsonl; empty disables)
- `DATING_TEMPERATURE` - LLM temperature parameter (default: 0.7)
- `DATING_PROMPT` - Custom prompt for message generation
- `DATING_MBTI_PROMPT` - Custom prompt for MBTI analysis
