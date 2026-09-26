// Command voicecli calls the speech and language providers directly: one-off calls while
// developing, spoken-clip generation, and the latency benchmark gate. Providers come from the
// environment (a .env file in the working directory is read first), so PROVIDERS=fake works
// without keys.
//
//	voicecli asr     --lang hi --wav question.wav
//	voicecli tts     --lang hi --text "नमस्ते" --out hello.wav [--rate 16000]
//	voicecli llm     --lang hi --text "भिन्न क्या है?" [--model gemini-3.5-flash-lite]
//	voicecli assets  [--langs hi,en] [--out internal/infrastructure/clips/assets]
//	voicecli latency --lang hi --wav question.wav [--runs 20]
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/bitwizard25/Shiksh_AI/internal/bootstrap"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
)

const usage = `usage:
  voicecli asr     --lang hi --wav question.wav                   transcribe a 16 kHz mono WAV
  voicecli tts     --lang hi --text "..." --out a.wav [--rate N]  synthesize speech into a WAV
  voicecli llm     --lang hi --text "..." [--model M]             stream a tutor reply
  voicecli assets  [--langs hi,en] [--out DIR]                    generate filler/repeat/error/redirect clips
  voicecli latency --lang hi --wav question.wav [--runs 20]       ASR -> LLM -> TTS latency percentiles`

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		fmt.Fprintln(os.Stderr, "voicecli: load .env:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], environ(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "voicecli:", err)
		os.Exit(1)
	}
}

// cli holds what every subcommand needs.
type cli struct {
	cfg  config.ProviderConfig
	prov bootstrap.Providers
	out  io.Writer
}

func run(ctx context.Context, args []string, env map[string]string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(out, usage)
		return nil
	}
	commands := map[string]func(*cli, context.Context, []string) error{
		"asr": (*cli).asr, "tts": (*cli).tts, "llm": (*cli).llm, "assets": (*cli).assets, "latency": (*cli).latency,
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	cfg, err := config.LoadProviderConfigFrom(env)
	if err != nil {
		return err
	}
	prov, err := bootstrap.BuildProviders(ctx, cfg, nil)
	if err != nil {
		return err
	}
	return cmd(&cli{cfg: cfg, prov: prov, out: out}, ctx, args[1:])
}

func environ() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[k] = v
		}
	}
	return m
}
