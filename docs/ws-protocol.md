# WebSocket protocol (`shiksha.v1`)

This is the contract between a client and the `realtime` role. The design reasoning is in
`docs/design.md` §8–§11; where this file and the design differ, this file describes what is built.

## Connecting

1. `POST /v1/sessions` (or `POST /v1/sessions/{id}/ticket` to reconnect) returns `ws: {url, ticket, expires_at}`.
2. Open `GET {url}?ticket=<ticket>` with the header `Sec-WebSocket-Protocol: shiksha.v1`.

| Response | Meaning |
|---|---|
| 101 | Connected. The first message is `session.ready`. |
| 400 | The `shiksha.v1` subprotocol was not offered. The ticket is **not** used up. |
| 401 | The ticket is unknown, already used, expired, or its session ended. |
| 503 + `Retry-After` | The server is shutting down. Retry shortly with the same ticket. |

Tickets live 60 seconds and work once. Compression is off. The server pings every 20 seconds; a
client that does not answer is dropped. Browsers must connect from an origin listed in `ALLOWED_ORIGINS`.

Opening a session that is already connected elsewhere **replaces** the old connection: it receives
`error session_replaced` and is closed with 4009.

## Client to server

**Binary:** PCM, signed 16-bit little-endian, mono, 16 kHz, 20–100 ms per frame. Frames are accepted
only between `speech.start` and `speech.end`; at any other time they are dropped.

**Text (JSON):** unknown fields and unknown types are rejected with `error bad_message`.

| `type` | Fields | Meaning |
|---|---|---|
| `speech.start` | `played?: {turn_id, chunk_seq}` | Speech was detected. If tutor audio was playing, stop playback at once and send `played`: the last tutor segment you **fully** played. Without `played`, nothing of that turn counts as heard. |
| `speech.end` | | The utterance is finished. |
| `speech.cancel` | | False alarm: discard the audio. |
| `text.input` | `text` (1–1000 characters) | A typed question. Stop playback yourself first. |
| `playback.started` | `turn_id` | The first tutor sound of the turn began playing. |
| `playback.done` | `turn_id` | The whole turn finished playing. Send it: without it, the next `speech.start` is treated as an interruption of that turn. |
| `session.end` | | End the session. |
| `ping` | `t?` (small JSON value) | Answered with `pong`. |

Audio shorter than 250 ms is discarded. Audio is cut at 30 s (`utterance_too_long`, then the turn
runs on what was heard). At most 30 turns a minute with a burst of 3 (`rate_limited`).

**Pauses:** if you send `speech.start` within 2 s of `speech.end` and no tutor audio has been sent
yet, the server treats it as the same sentence continuing: it drops the in-flight turn and merges
the audio. A new `turn.started` follows at the next `speech.end`.

## Server to client

**Text (JSON):** every message has `type`. Messages about a turn carry `turn_id`; ignore any whose
`turn_id` is not your current turn.

| `type` | Fields |
|---|---|
| `session.ready` | `session_id, language, subject, resumed, next_turn_id, audio_in{encoding,sample_rate,channels}, audio_out{encoding,channels}, limits{max_utterance_ms,idle_timeout_ms}, history[]` (`history` has `turn_no, role, content, status`, and is filled only when `resumed`) |
| `turn.started` | `turn_id, input_mode` (`voice` or `text`), sent at `speech.end` / `text.input` |
| `transcript` | `turn_id, text` |
| `transcript.empty` | `turn_id`: nothing usable was heard; a "please repeat" clip follows |
| `tutor.segment` | `turn_id, chunk_seq, text, sample_rate`: the caption, sent just before that segment's audio |
| `tutor.done` | `turn_id, status` (`complete`, `interrupted`, `failed`), `latency{asr_ms, llm_ttft_ms, first_segment_ms, first_tts_ms, first_audio_ms, client_playback_ms, total_ms, filler}` |
| `error` | `code, message, retryable, turn_id?` |
| `session.ended` | `reason`: `learner`, `idle`, `max_duration` |
| `pong` | `t` echoed |

All latency figures are milliseconds from the end of the learner's speech. `first_audio_ms` is when
the first tutor audio frame was **queued** on the server; `client_playback_ms` is filled in from
`playback.started` and only reaches metrics, not the stored row.

**Error codes:** `asr_failed`, `llm_failed`, `tts_failed` (the caption still arrives; that segment is silent),
`utterance_too_long`, `rate_limited`, `bad_message`, `storage_unavailable`, `server_shutdown`
(`retryable`: reconnect with a new ticket), `session_replaced`.

**Binary:** a 12-byte header, then raw PCM s16le mono, about 200 ms per frame.

```
[0]      kind        1 = tutor speech, 2 = filler, 3 = system clip (repeat / error / redirect)
[1]      flags       bit 0 = last frame of the segment, bit 1 = last frame of the turn
[2..5]   turn_id     uint32, big endian
[6..7]   chunk_seq   uint16, the segment number within the turn
[8..9]   part        uint16, the frame number within the segment
[10..11] sample_rate uint16, e.g. 22050 (every frame says it, so the rate may differ per clip)
[12..]   PCM
```

The "last frame of the turn" bit is best effort: it is set only if the server already knew that no
more segments would follow. Use `tutor.done` to know a turn is finished.

## Turn ids and resuming

`turn_id` is a number that grows by one per turn on a connection. On every `session.ready`, reset
your turn state: `next_turn_id` is the id of the next turn. The stored turn number (`turn_no`) is
separate and continues across reconnects.

A reconnect (`resumed: true`) restores the conversation from the database. Whatever the learner
had not heard when the connection dropped is stored as an interrupted reply.

## Interrupting the tutor

TTS runs ahead of playback, so by the time the learner speaks the server has often sent the whole reply.
Send `speech.start` with `played` as soon as speech is detected:

- while the turn is still running, the server stops it, keeps only the text up to `played`, and sends `tutor.done` with `status: interrupted`;
- after the turn finished, the stored reply is cut down to the heard text (unless you already sent `playback.done`).

## Client requirements

- Capture with echo cancellation on, or the tutor will hear itself.
- Stop a filler (kind 2) on the first kind-1 frame.
- Drop audio frames and captions whose `turn_id` is not current.
- Send `playback.done` when a turn finishes playing.

## Closing

| Code | Meaning | Reconnect? |
|---|---|---|
| 1000 | Normal: the session ended (`session.ended` came first) | No |
| 1001 | Server shutting down (after `error server_shutdown`) | Yes, with a new ticket |
| 1008 | Policy: the client could not keep up with the output | Yes |
| 1011 | Server storage problem (`error storage_unavailable`) | Yes |
| 4008 | Idle for 3 minutes (the session stays open for resuming until the sweeper ends it) | Yes |
| 4009 | Replaced by a newer connection, or fenced | No: another connection has the session |
| 4010 | The session has ended or does not exist | No |
