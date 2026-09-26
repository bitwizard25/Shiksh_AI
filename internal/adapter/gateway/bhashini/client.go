// Package bhashini implements the ASR and TTS ports on Bhashini's ULCA pipeline API. A config call
// resolves, per language, the service id plus the inference endpoint and key; compute calls then
// send audio or text. Resolved services are cached and refreshed in the background.
package bhashini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

const (
	provider         = "bhashini"
	taskASR          = "asr"
	taskTTS          = "tts"
	maxResponseBytes = 10 << 20
)

// Config configures the Bhashini client.
type Config struct {
	UserID       string
	ULCAKey      string
	PipelineID   string
	ConfigURL    string
	HTTPClient   *http.Client  // nil uses http.DefaultClient
	CacheTTL     time.Duration // default 6h
	RetryBackoff time.Duration // first delay between Warm retries; default 1s, doubling up to 5m
	Logger       *slog.Logger  // nil discards
}

// Client talks to Bhashini. It is safe for concurrent use.
type Client struct {
	cfg Config
	now func() time.Time
	sf  singleflight.Group

	mu         sync.RWMutex
	cache      map[cacheKey]service
	available  map[string]bool
	refreshing map[string]bool
}

type cacheKey struct{ task, lang string }

type service struct {
	ServiceID   string
	CallbackURL string
	AuthName    string
	AuthValue   string
	fetchedAt   time.Time
}

var (
	_ conversation.ASR = (*Client)(nil)
	_ conversation.TTS = (*Client)(nil)
)

// New builds a client. Nothing is fetched until the first call or Warm.
func New(cfg Config) *Client {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 6 * time.Hour
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Client{cfg: cfg, now: time.Now, cache: map[cacheKey]service{}, available: map[string]bool{}, refreshing: map[string]bool{}}
}

// Available reports whether both ASR and TTS resolved for the language.
func (c *Client) Available(code string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.available[code]
}

// Endpoints returns the distinct inference URLs resolved so far, for connection warming.
func (c *Client) Endpoints() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for _, svc := range c.cache {
		if svc.CallbackURL != "" && !slices.Contains(out, svc.CallbackURL) {
			out = append(out, svc.CallbackURL)
		}
	}
	return out
}

// Warm resolves every language in the background and returns immediately. A language whose config
// call fails is retried with exponential backoff (capped at 5 minutes) until it succeeds or ctx is
// done; a successful call that lacks ASR or TTS for the language marks it unavailable for good.
func (c *Client) Warm(ctx context.Context, langs []string) {
	for _, lang := range langs {
		go c.warm(ctx, lang)
	}
}

func (c *Client) warm(ctx context.Context, lang string) {
	backoff := c.cfg.RetryBackoff
	for {
		err := c.fetch(ctx, lang)
		if err == nil {
			if !c.Available(lang) {
				c.cfg.Logger.Info("bhashini pipeline does not offer ASR and TTS for this language; it stays unavailable", "lang", lang)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		c.cfg.Logger.Warn("bhashini config fetch failed; retrying", "lang", lang, "err", err, "retry_in", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(2*backoff, 5*time.Minute)
	}
}

// resolve returns the service for task in lang. A cached entry is always served; once it is older
// than 80 % of CacheTTL a background refresh starts, and the stale entry keeps serving if that fails.
func (c *Client) resolve(ctx context.Context, task, lang string) (service, error) {
	key := cacheKey{task, lang}
	c.mu.RLock()
	svc, ok := c.cache[key]
	c.mu.RUnlock()
	if ok {
		if c.now().Sub(svc.fetchedAt) > c.cfg.CacheTTL*8/10 {
			c.refreshInBackground(lang)
		}
		return svc, nil
	}
	if err := c.fetch(ctx, lang); err != nil {
		return service{}, err
	}
	c.mu.RLock()
	svc, ok = c.cache[key]
	c.mu.RUnlock()
	if !ok {
		return service{}, &conversation.ProviderError{Provider: provider, Op: task, Err: fmt.Errorf("%w: %s for %q", conversation.ErrLanguageUnsupported, task, lang)}
	}
	return svc, nil
}

func (c *Client) refreshInBackground(lang string) {
	c.mu.Lock()
	if c.refreshing[lang] {
		c.mu.Unlock()
		return
	}
	c.refreshing[lang] = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.refreshing, lang)
			c.mu.Unlock()
		}()
		_ = c.fetch(context.Background(), lang) // on failure the stale entry keeps serving
	}()
}

func (c *Client) invalidate(task, lang string) {
	c.mu.Lock()
	delete(c.cache, cacheKey{task, lang})
	c.mu.Unlock()
}

// fetch runs the config call for lang (ASR + TTS together) and stores what it resolves. Concurrent
// fetches of one language share a request that outlives any single caller's cancellation, bounded
// by 30 s.
func (c *Client) fetch(ctx context.Context, lang string) error {
	ch := c.sf.DoChan(lang, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		req := configRequest{
			PipelineTasks: []pipelineTask{
				{TaskType: taskASR, Config: taskConfig{Language: language{SourceLanguage: lang}}},
				{TaskType: taskTTS, Config: taskConfig{Language: language{SourceLanguage: lang}}},
			},
			PipelineRequestConfig: pipelineRequestConfig{PipelineID: c.cfg.PipelineID},
		}
		var resp configResponse
		headers := map[string]string{"userID": c.cfg.UserID, "ulcaApiKey": c.cfg.ULCAKey}
		if err := c.post(fctx, "config", c.cfg.ConfigURL, headers, req, &resp); err != nil {
			return nil, err
		}
		c.store(lang, resp)
		return nil, nil
	})
	select {
	case r := <-ch:
		return r.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) store(lang string, resp configResponse) {
	ep := resp.PipelineInferenceAPIEndPoint
	now := c.now()
	found := map[string]bool{}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, task := range resp.PipelineResponseConfig {
		for _, sc := range task.Config {
			if sc.Language.SourceLanguage != lang || sc.ServiceID == "" {
				continue
			}
			c.cache[cacheKey{task.TaskType, lang}] = service{
				ServiceID: sc.ServiceID, CallbackURL: ep.CallbackURL,
				AuthName: ep.InferenceAPIKey.Name, AuthValue: ep.InferenceAPIKey.Value, fetchedAt: now,
			}
			found[task.TaskType] = true
			break
		}
	}
	c.available[lang] = found[taskASR] && found[taskTTS] && ep.CallbackURL != ""
}

// compute resolves the service, runs one compute call, and on 401/403 invalidates the cached entry
// and retries once with a freshly fetched inference key.
func (c *Client) compute(ctx context.Context, task, lang string, build func(service) computeRequest, out *computeResponse) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var svc service
		if svc, err = c.resolve(ctx, task, lang); err != nil {
			return err
		}
		err = c.post(ctx, task, svc.CallbackURL, map[string]string{svc.AuthName: svc.AuthValue}, build(svc), out)
		var pe *conversation.ProviderError
		if attempt == 0 && errors.As(err, &pe) && (pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden) {
			c.invalidate(task, lang)
			continue
		}
		return err
	}
	return err
}

// post sends a JSON request and decodes the JSON response. Caller cancellation is returned as the
// plain context error; everything else becomes a *conversation.ProviderError without secrets.
func (c *Client) post(ctx context.Context, op, url string, headers map[string]string, reqBody, respBody any) error {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return &conversation.ProviderError{Provider: provider, Op: op, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &conversation.ProviderError{Provider: provider, Op: op, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return &conversation.ProviderError{Provider: provider, Op: op, Retryable: true, Err: errors.New(redactHeaders(redactURL(err.Error(), url), headers))}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return &conversation.ProviderError{Provider: provider, Op: op, Status: resp.StatusCode, Retryable: true, Err: err}
	}
	if len(data) > maxResponseBytes {
		return &conversation.ProviderError{Provider: provider, Op: op, Status: resp.StatusCode, Err: errors.New("response larger than 10 MB")}
	}
	if resp.StatusCode/100 != 2 {
		return &conversation.ProviderError{
			Provider: provider, Op: op, Status: resp.StatusCode,
			Retryable: resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
			Err:       errors.New(redactHeaders(snippet(data), headers)),
		}
	}
	if err := json.Unmarshal(data, respBody); err != nil {
		return &conversation.ProviderError{Provider: provider, Op: op, Status: resp.StatusCode, Err: fmt.Errorf("decode response: %w", err)}
	}
	return nil
}

// snippet returns the first 200 characters of a response body on one line.
func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// redactURL removes the query string of url from msg (transport errors quote the full URL).
func redactURL(msg, url string) string {
	if i := strings.IndexByte(url, '?'); i >= 0 {
		return strings.ReplaceAll(msg, url, url[:i])
	}
	return msg
}

// redactHeaders replaces every non-empty header value in msg with "[redacted]". The ULCA key and
// the Bhashini inference key travel as header values, so a provider that echoes the request (an
// error body, or a transport error that quotes it) never leaks them.
func redactHeaders(msg string, headers map[string]string) string {
	for _, v := range headers {
		if v != "" {
			msg = strings.ReplaceAll(msg, v, "[redacted]")
		}
	}
	return msg
}
