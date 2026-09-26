package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

const asrRate = 16000 // the ASR port's input rate (the client uplink format)

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s: %w\n%s", fs.Name(), err, usage)
	}
	return nil
}

func lookupLang(code string) (entity.Language, error) {
	l, ok := entity.LookupLanguage(code)
	if !ok {
		return entity.Language{}, fmt.Errorf("unsupported language %q", code)
	}
	return l, nil
}

// readQuestion loads a 16 kHz mono 16-bit WAV, the format the ASR port takes.
func readQuestion(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pcm, f, err := audio.DecodeWAV(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.SampleRate != asrRate || f.Channels != 1 {
		return nil, fmt.Errorf("%s is %d Hz with %d channel(s); ASR needs 16 kHz mono 16-bit PCM. Convert it with:\n  ffmpeg -i %s -ar 16000 -ac 1 -c:a pcm_s16le out.wav",
			path, f.SampleRate, f.Channels, path)
	}
	return pcm, nil
}

func writeWAV(path string, pcm []byte, rate int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, audio.EncodeWAV(pcm, rate, 1), 0o644)
}

func ms(d time.Duration) int { return int(d.Milliseconds()) }

func (c *cli) asr(ctx context.Context, args []string) error {
	fs := newFlags("asr")
	lang := fs.String("lang", "hi", "language code")
	wav := fs.String("wav", "", "16 kHz mono WAV to transcribe")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *wav == "" {
		return errors.New("asr: --wav is required")
	}
	l, err := lookupLang(*lang)
	if err != nil {
		return err
	}
	pcm, err := readQuestion(*wav)
	if err != nil {
		return err
	}
	start := time.Now()
	res, err := c.prov.ASR.Transcribe(ctx, conversation.ASRRequest{Lang: l.Code, PCM: pcm, SampleRate: asrRate})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "transcript: %q\nasr_ms: %d (audio %d ms)\n", res.Text, ms(time.Since(start)), audio.DurationMs(len(pcm), asrRate, 1))
	return nil
}

func (c *cli) tts(ctx context.Context, args []string) error {
	fs := newFlags("tts")
	lang := fs.String("lang", "hi", "language code")
	text := fs.String("text", "", "text to speak")
	out := fs.String("out", "", "output WAV path")
	rate := fs.Int("rate", 0, "resample to this rate (0 keeps the provider's rate)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *text == "" || *out == "" {
		return errors.New("tts: --text and --out are required")
	}
	l, err := lookupLang(*lang)
	if err != nil {
		return err
	}
	start := time.Now()
	res, err := c.prov.TTS.Synthesize(ctx, conversation.TTSRequest{Lang: l.Code, Text: *text, Gender: l.TTSGender})
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	pcm, sr := res.PCM, res.SampleRate
	if *rate > 0 && *rate != sr {
		pcm, sr = audio.Resample(pcm, sr, *rate), *rate
	}
	if err := writeWAV(*out, pcm, sr); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "wrote %s: %d Hz, %d ms (provider rate %d Hz)\ntts_ms: %d\n", *out, sr, audio.DurationMs(len(pcm), sr, 1), res.SampleRate, ms(elapsed))
	return nil
}

func (c *cli) llm(ctx context.Context, args []string) error {
	fs := newFlags("llm")
	lang := fs.String("lang", "hi", "language code")
	text := fs.String("text", "", "the learner's question")
	model := fs.String("model", "", "model override (default GEMINI_MODEL)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *text == "" {
		return errors.New("llm: --text is required")
	}
	l, err := lookupLang(*lang)
	if err != nil {
		return err
	}
	start := time.Now()
	ttft := -1
	for d, err := range c.prov.LLM.Stream(ctx, tutorRequest(l, *text, *model)) {
		if err != nil {
			return err
		}
		if ttft < 0 {
			ttft = ms(time.Since(start))
		}
		fmt.Fprint(c.out, d.Text)
		if d.Blocked {
			fmt.Fprintf(c.out, " [blocked: %s]", d.FinishReason)
		}
	}
	fmt.Fprintf(c.out, "\nllm_ttft_ms: %d\nllm_total_ms: %d\n", ttft, ms(time.Since(start)))
	return nil
}

// tutorRequest stands in for the Plan 3 tutor prompt: close enough to measure realistic replies.
func tutorRequest(l entity.Language, question, model string) conversation.LLMRequest {
	return conversation.LLMRequest{
		Model: model,
		System: fmt.Sprintf("You are Shiksha, a warm and patient tutor for Indian school students. Reply only in %s, in its native script (%s). "+
			"Your reply is spoken aloud: one to three short sentences, no markdown, lists, emoji or symbols, and numbers written as words. "+
			"Keep the first sentence short and end with one question that checks understanding.", l.Name, l.NativeName),
		Messages:        []conversation.ChatMessage{{Role: conversation.RoleUser, Text: question}},
		MaxOutputTokens: 400,
	}
}

func (c *cli) assets(ctx context.Context, args []string) error {
	fs := newFlags("assets")
	langs := fs.String("langs", strings.Join(c.cfg.EnabledLanguages, ","), "comma-separated language codes")
	dir := fs.String("out", filepath.Join("internal", "infrastructure", "clips", "assets"), "output directory")
	if err := parse(fs, args); err != nil {
		return err
	}
	for _, code := range strings.Split(*langs, ",") {
		l, err := lookupLang(strings.TrimSpace(code))
		if err != nil {
			return err
		}
		for _, clip := range clipTexts(l) {
			res, err := c.prov.TTS.Synthesize(ctx, conversation.TTSRequest{Lang: l.Code, Text: clip.text, Gender: l.TTSGender})
			if err != nil {
				return fmt.Errorf("%s/%s: %w", l.Code, clip.file, err)
			}
			path := filepath.Join(*dir, l.Code, clip.file)
			if err := writeWAV(path, res.PCM, res.SampleRate); err != nil {
				return err
			}
			note := ""
			if strings.HasPrefix(clip.file, "filler_") && res.DurationMs > 400 {
				note = "  <- longer than 400 ms; spec §13 wants short fillers"
			}
			fmt.Fprintf(c.out, "%s\t%5d ms\t%d Hz\t%q%s\n", path, res.DurationMs, res.SampleRate, clip.text, note)
		}
	}
	return nil
}

type clip struct{ file, text string }

// clipTexts lists a language's fixed phrases with their clip file names.
func clipTexts(l entity.Language) []clip {
	var clips []clip
	for i, f := range l.Phrases.Fillers {
		clips = append(clips, clip{fmt.Sprintf("filler_%d.wav", i+1), f})
	}
	return append(clips, clip{"repeat.wav", l.Phrases.Repeat}, clip{"error.wav", l.Phrases.Error}, clip{"redirect.wav", l.Phrases.Redirect})
}
