# tg-dating-agent

Go userbot processes `@leomatchbot` profiles; a separate HTTP forwarder delivers mutual-like notifications through Telegram Bot API.

## Map
- `cmd/dating/main.go` — resolve LLM configuration before Telegram startup; wire handlers and shutdown.
- `internal/standalone/` — environment loading and existing Telegram authorization.
- `internal/llm/routing.go` — startup OpenCode metadata resolution; never replace configured inference URLs with catalog URLs.
- `internal/llm/client.go`, `native.go`, `decision.go` — multimodal transports and strict decision parsing.
- `internal/dating/dating.go`, `state.go` — sequential profile worker, pending message, retries and lifecycle ownership.
- `internal/dating/reciprocal_payload.go`, `webhook_client.go` — mutual-like payload and outbound delivery.
- `internal/dating/reply_audit_logger.go` — local JSONL and R2 audit records.
- `internal/forwarder/` — webhook validation and target-chat delivery; producer and consumer payload changes must agree.
- `internal/storage/` — optional instance-scoped R2 persistence.

## Invariants
- Selection belongs to `DATING_PROMPT`; technical duplicate/own-profile/stale-event guards remain independent.
- A validated `send` permits only its message: one line, nonempty, at most 200 Unicode characters. `skip` requires an empty message.
- Never parse free-form reasoning as a decision or send it as an opener; structured output failures cannot authorize a like.
- Corrections retain original criteria and image context. No generic greeting or truncation fallback.
- No proven Telegram message-entry cancellation exists: exhausted corrections after entry stop locally, never send a button as opener text.
- Temporary files are cleaned after preparation; active image data stays available for retries and is released with its profile context.
- Preserve queue ordering, message-ID correlation, state mutex protection and context cancellation during send/shutdown.
- Cache only valid decisions; do not persist provider failures as selection decisions.
- Direct OpenCode resolves once from live model IDs and catalog protocol metadata before processing profiles; no model-name routing lists or endpoint probing.
- Custom gateways receive model IDs unchanged. Public metadata requests must not receive API keys; metadata must not redirect inference credentials.
- Audit `sent` means successful Telegram API delivery, not later acceptance by the dating bot. Historical audit/dedupe records are not migrated destructively.
- Forwarder auth is independent of Telegram Bot API credentials; preserve multipart bounds and instance-scoped storage keys.
- Docker is non-root with a read-only root filesystem. Do not fix storage errors by weakening these defaults.
- Never commit `.env`, Telegram sessions, downloaded photos or credentials.

## Verify
- `go test ./...` — shared contract and entrypoint changes.
- `go test -race ./internal/dating ./internal/llm` — concurrency/lifecycle changes.
- If local environment contaminates config tests: `env -i HOME="$HOME" PATH="$PATH" go test ./...`.
- Live inference and Telegram sending are external actions, not default test gates.

## Docs
- `README.md`, `env.example` — API configuration, prompt protocol and webhook setup.
- `docker-compose.yml`, `docker-compose.forwarder.yml` — actual environment files, bind addresses and mounts; check before giving deployment commands.
- Commit/push does not deploy: server checkout and running image must be updated separately.
