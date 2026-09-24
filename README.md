# Shiksha AI

Shiksha AI is a voice-first tutor for Indian-language learners: speech in, model reasoning, speech out. The turn structure and latency budget are designed so a session feels like talking to a person. This repository is the **Go backend**.

- Design (HLD + LLD + diagrams): [docs/design.md](docs/design.md). §19 covers architecture and scaling.
- Implementation plans: [docs/superpowers/plans/](docs/superpowers/plans/)

## Architecture

The code follows classic **Clean Architecture** layers. Source dependencies point inward only, and `internal/archtest` fails the build if they don't.

| Layer | Package | Holds |
|---|---|---|
| Entities | `internal/entity` | Enterprise rules: users, emails, languages, refresh-token replay rule |
| Use cases | `internal/usecase` | Interactors (auth, accounts) and the ports they need |
| Interface adapters | `internal/adapter` | REST controllers (`httpapi`) and Postgres repositories (`repository`) |
| Frameworks & drivers | `internal/infrastructure` | Config, database (pool, TxManager, migrations), crypto, mail, rate limiter |
| Composition root | `internal/bootstrap`, `cmd/shiksha` | Wiring and roles |

The same binary runs as different **roles**, so each part scales on its own. Coordination goes only through Postgres; no Redis is needed.

```bash
shiksha serve --roles=api      # stateless REST API (this plan)
shiksha serve                  # every role in this build (local dev)
shiksha migrate                # apply migrations and exit (release step)
```

## Status

| Plan | Scope | State |
|---|---|---|
| 1 | Clean skeleton, Postgres, accounts API, admin endpoints, `api` role | ✅ this branch |
| 2 | Bhashini ASR/TTS + Gemini gateways, latency benchmark gate | next |
| 3 | Tutoring sessions, prompts, summaries; `worker` role (River jobs), LISTEN/NOTIFY bus | planned |
| 4 | `realtime` role: WebSocket voice core (turn pipeline, barge-in) | planned |
| 5 | Latency polish, hardening, protocol docs | planned |

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
| GET | `/v1/languages` | – |
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
