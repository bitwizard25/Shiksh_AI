# Shiksha AI

Shiksha AI is a voice-first tutor for Indian-language learners: speech in, model reasoning, speech out. The turn structure and latency budget are designed so a session feels like talking to a person. This repository is the **Go backend**.

- Design (HLD + LLD + diagrams): [docs/design.md](docs/design.md). §19 covers architecture and scaling.
- The build is delivered in five plans; see [Status](#status).

## Architecture

The code follows classic **Clean Architecture** layers. Source dependencies point inward only, and `internal/archtest` fails the build if they don't.

| Layer | Package | Holds |
|---|---|---|
| Entities | `internal/entity` | Enterprise rules: users, emails, languages (with each one's TTS voice and spoken phrases), refresh-token replay rule |
| Use cases | `internal/usecase` | Interactors (auth, accounts, language catalog) and the ports they need; `usecase/conversation` holds the speech and language ports (ASR, LLM, TTS) |
| Interface adapters | `internal/adapter` | REST controllers (`httpapi`), Postgres repositories (`repository`), provider gateways (`gateway`: Bhashini, Gemini, simulated providers, and a guard with a concurrency cap, circuit breaker and metrics) |
| Frameworks & drivers | `internal/infrastructure` | Config, database (pool, TxManager, migrations), crypto, mail, rate limiter, audio (WAV/PCM) |
| Composition root | `internal/bootstrap`, `cmd/shiksha` | Wiring and roles |
| Tools | `cmd/voicecli`, `cmd/devdb` | Provider CLI and latency benchmark; local Postgres for development |

The same binary runs as different **roles**, so each part scales on its own. Coordination goes only through Postgres; no Redis is needed.

```bash
shiksha serve --roles=api      # stateless REST API
shiksha serve                  # every role in this build (currently just api)
shiksha migrate                # apply migrations and exit (release step)
```

The `worker` role arrives in Plan 3 and the `realtime` (voice WebSocket) role in Plan 4.

## Status

| Plan | Scope | State |
|---|---|---|
| 1 | Clean skeleton, Postgres, accounts API, admin endpoints, `api` role | ✅ done |
| 2 | Bhashini ASR/TTS + Gemini gateways, provider guard, `voicecli`, latency benchmark gate | ✅ code done; the real-provider latency benchmark is pending API keys |
| 3 | Tutoring sessions, prompts, summaries; `worker` role (River jobs), LISTEN/NOTIFY bus | next (planned in detail) |
| 4 | `realtime` role: WebSocket voice core (turn pipeline, barge-in) | planned |
| 5 | Latency polish, hardening, protocol docs | planned |

**Works today:**
- Accounts: sign-up, sign-in, rotating refresh tokens, password reset, profile, account deletion.
- The language catalog, which shows each language's availability.
- The speech and language provider layer (Bhashini and Gemini, or simulated providers), exercised through `voicecli`.

**Not yet:** tutoring sessions (Plan 3) and the live voice conversation (Plan 4).

## Run locally (Windows, no Docker)

You need Go 1.27. In the first terminal, start a local Postgres (the first run downloads it):

```powershell
go run ./cmd/devdb
```

In a second terminal:

```powershell
Copy-Item .env.example .env
# .env.example ships with no JWT_SECRET (there is no safe default). Generate one and set it in .env:
[Convert]::ToBase64String((1..48 | % { Get-Random -Max 256 }))
go run ./cmd/shiksha serve
```

Try it:

```powershell
$body = @{ email="asha@example.com"; password="correct horse"; display_name="Asha"; terms_accepted=$true } | ConvertTo-Json
$r = Invoke-RestMethod -Method Post -Uri http://localhost:8080/v1/auth/register -ContentType 'application/json' -Body $body
Invoke-RestMethod -Uri http://localhost:8080/v1/me -Headers @{ Authorization = "Bearer $($r.access_token)" }
Invoke-RestMethod -Uri http://localhost:9090/readyz
```

## Run with Docker

```bash
cp .env.example .env
# .env.example ships with no JWT_SECRET (there is no safe default). Generate one and set it in .env:
openssl rand -base64 48
docker compose up --build
```

`docker-compose.yml` runs `serve --roles=api` with migrations on (`--migrate` defaults to
`true`), which is convenient for local dev. The production image's default `CMD` is
`serve --migrate=false`: a service instance should never race another replica to apply
migrations at startup. Run `shiksha migrate` once as its own release step instead, using a
direct (non-PgBouncer) `DATABASE_URL` — migrations need a real session, not a pooled
transaction-mode connection.

## Tests

```bash
go test -race ./...
```

- **Use-case tests** run on in-memory fakes, with no database.
- **Adapter and infrastructure tests** start a real embedded Postgres, with no Docker. The first run downloads the binaries; run `go test ./internal/infrastructure/database/...` once on its own before the full suite, so the download doesn't race across packages.
- **Existing server:** set `TEST_DATABASE_URL` to a role with `CREATEDB` to test against it instead.

## Speech and language providers

`PROVIDERS=fake` (the default) simulates speech recognition, the tutor model and speech synthesis, so everything runs without keys. `PROVIDERS=real` uses Bhashini (ASR + TTS) and Gemini and needs `BHASHINI_USER_ID`, `BHASHINI_ULCA_API_KEY` and `GEMINI_API_KEY` in `.env`. Use a paid-tier Gemini key: the learners are minors, and paid-tier data is not used for training. `GET /v1/languages` reports `available: false` for a language until Bhashini has resolved both its ASR and TTS models; failed attempts are logged and retried in the background.

Every provider call goes through a guard with three parts:
- **A concurrency cap:** `PROVIDER_MAX_CONCURRENCY`, default 64.
- **A circuit breaker:** after 5 consecutive failures it fails fast for 15 s, then lets one probe call through.
- **Prometheus metrics** on `:9090/metrics`.

API keys never appear in logs or error messages.

`voicecli` calls the providers directly:

```bash
go run ./cmd/voicecli tts --lang hi --text "नमस्ते" --out hello.wav
go run ./cmd/voicecli asr --lang hi --wav testdata/hi_question.wav       # 16 kHz mono WAV
go run ./cmd/voicecli llm --lang hi --text "भिन्न क्या होता है?"
go run ./cmd/voicecli assets                                              # regenerate the spoken clips
go run ./cmd/voicecli latency --lang hi --wav testdata/hi_question.wav --runs 20
```

The benchmark's p50/p95 results against the latency budget (p50 ≤ 1.5 s, p95 ≤ 2.5 s) will be recorded in [docs/design.md](docs/design.md) §20 once it has run with real keys.

## API (current)

| Method | Path | Auth |
|---|---|---|
| POST | `/v1/auth/register` | – |
| POST | `/v1/auth/login` | – |
| POST | `/v1/auth/refresh` | – |
| POST | `/v1/auth/logout` | – |
| POST | `/v1/auth/password/forgot` | – |
| POST | `/v1/auth/password/reset` | – |
| GET / PATCH / DELETE | `/v1/me` | Bearer |
| GET | `/v1/languages` (with `available`) | – |
| GET | `:9090/healthz`, `:9090/readyz`, `:9090/metrics` | – |

Errors use `{"error":{"code","message","field?","request_id"}}`.

## Upcoming features

- **Structured lessons:** topic catalogue, lesson plans, tutor-driven steps, quizzes, progress tracking.
- **Language-learning mode:** pronunciation feedback and conversation practice.
- **Streaming ASR** through Bhashini's socket.io API, so the learner is transcribed while they speak.
- **Gemini Live** native-audio engine as an alternative pipeline.
- **Opus audio downlink**, or resampling, to cut mobile bandwidth.
- **Phone OTP and Google sign-in.**
- **Verifiable parental consent** (DPDP Act 2023).
- **Parent and teacher dashboards** with learning analytics.
- **Opt-in audio retention** for quality review.
- **Usage quotas and plans.**
- **Redis adapters** for globally exact rate limits, plus **OpenTelemetry tracing**.
- **Web and mobile clients.**
