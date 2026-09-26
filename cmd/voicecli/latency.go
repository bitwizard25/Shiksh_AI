package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// stages in report order, with the design §2 p50 budget in ms.
var stages = []struct {
	name   string
	budget int
}{
	{"asr_ms", 450}, {"llm_ttft_ms", 350}, {"first_clause_ms", 500}, {"tts_first_ms", 350}, {"server_total_ms", 1500},
}

func (c *cli) latency(ctx context.Context, args []string) error {
	fs := newFlags("latency")
	lang := fs.String("lang", "hi", "language code")
	wav := fs.String("wav", "", "16 kHz mono WAV with a spoken question")
	runs := fs.Int("runs", 20, "measured runs (after one warm-up run)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *wav == "" || *runs < 1 {
		return errors.New("latency: --wav is required and --runs must be at least 1")
	}
	l, err := lookupLang(*lang)
	if err != nil {
		return err
	}
	pcm, err := readQuestion(*wav)
	if err != nil {
		return err
	}
	samples := map[string][]int{}
	for i := 0; i <= *runs; i++ {
		m, err := c.measure(ctx, l, pcm)
		if err != nil {
			return fmt.Errorf("run %d: %w", i, err)
		}
		if i == 0 {
			fmt.Fprintf(c.out, "warm-up: transcript %q; first clause %q\n", m.transcript, m.clause)
			continue
		}
		for k, v := range m.ms {
			samples[k] = append(samples[k], v)
		}
	}
	fmt.Fprintf(c.out, "\nlang=%s providers=%s runs=%d\n%-16s %6s %6s %8s\n", l.Code, c.cfg.Mode, *runs, "stage", "p50", "p95", "budget")
	for _, s := range stages {
		fmt.Fprintf(c.out, "%-16s %6d %6d %8d\n", s.name, percentile(samples[s.name], 50), percentile(samples[s.name], 95), s.budget)
	}
	p50, p95 := percentile(samples["server_total_ms"], 50), percentile(samples["server_total_ms"], 95)
	verdict := "PASS"
	if p50 > 1500 || p95 > 2500 {
		verdict = "MISS: apply the design §2 levers in order"
	}
	fmt.Fprintf(c.out, "SLO server_total p50 <= 1500 ms and p95 <= 2500 ms: %s\n", verdict)
	return nil
}

type measurement struct {
	transcript, clause string
	ms                 map[string]int
}

// measure runs one turn the way the Plan 4 pipeline will: ASR, the LLM stream until the first
// speakable clause, then TTS of that clause.
func (c *cli) measure(ctx context.Context, l entity.Language, pcm []byte) (measurement, error) {
	m := measurement{ms: map[string]int{}}
	start := time.Now()
	res, err := c.prov.ASR.Transcribe(ctx, conversation.ASRRequest{Lang: l.Code, PCM: pcm, SampleRate: asrRate})
	if err != nil {
		return m, fmt.Errorf("asr: %w", err)
	}
	if res.Text == "" {
		return m, errors.New("asr heard nothing; check the recording")
	}
	m.transcript = res.Text
	m.ms["asr_ms"] = ms(time.Since(start))

	llmStart := time.Now()
	var text strings.Builder
	for d, err := range c.prov.LLM.Stream(ctx, tutorRequest(l, res.Text, "")) {
		if err != nil {
			return m, fmt.Errorf("llm: %w", err)
		}
		if _, seen := m.ms["llm_ttft_ms"]; !seen {
			m.ms["llm_ttft_ms"] = ms(time.Since(llmStart))
		}
		text.WriteString(d.Text)
		if clause, ok := firstClause(text.String()); ok {
			m.clause = clause
			break // the rest of the reply streams while the first clause is spoken
		}
	}
	if m.clause == "" {
		m.clause = strings.TrimSpace(text.String())
	}
	if m.clause == "" {
		return m, errors.New("llm returned no text")
	}
	m.ms["first_clause_ms"] = ms(time.Since(llmStart))

	ttsStart := time.Now()
	if _, err := c.prov.TTS.Synthesize(ctx, conversation.TTSRequest{Lang: l.Code, Text: m.clause, Gender: l.TTSGender}); err != nil {
		return m, fmt.Errorf("tts: %w", err)
	}
	m.ms["tts_first_ms"] = ms(time.Since(ttsStart))
	m.ms["server_total_ms"] = ms(time.Since(start))
	return m, nil
}

// firstClause applies the segmenter's first-segment rule (design §10) to streamed text: a hard
// stop (. ? ! । ॥) followed by whitespace after at least 2 runes, a newline, or a soft stop
// (, ; : —) after at least 20 runes. ok is false until such a boundary has arrived.
func firstClause(text string) (string, bool) {
	runes := []rune(text)
	for i, r := range runes {
		n := i + 1
		switch {
		case strings.ContainsRune(".?!।॥", r) && n >= 2 && i+1 < len(runes) && unicode.IsSpace(runes[i+1]):
			return strings.TrimSpace(string(runes[:n])), true
		case r == '\n' && strings.TrimSpace(string(runes[:i])) != "":
			return strings.TrimSpace(string(runes[:i])), true
		case strings.ContainsRune(",;:—", r) && n >= 20:
			return strings.TrimSpace(string(runes[:n])), true
		}
	}
	return "", false
}

// percentile returns the nearest-rank p-th percentile of v (0 for no samples).
func percentile(v []int, p int) int {
	if len(v) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(v))
	rank := (p*len(s) + 99) / 100 // ceil(p% of n)
	return s[max(rank, 1)-1]
}
