// Package conversation is the realtime use case: one live voice or text tutoring session.
//
// A Session is an actor. Its Run loop owns all session state and never blocks: it reads events
// (client messages, audio, finished turns, timers) from one channel, writes to the client only
// through Output.TrySend and to the database only through the Persister's queue. Each learner
// turn runs in its own goroutine (see turn.go) and talks to the actor through the same channel.
//
// This package imports only the standard library, uuid, entity and usecase. Providers (ASR,
// LLM, TTS), clips, the socket and metrics are ports implemented by outer layers.
package conversation
