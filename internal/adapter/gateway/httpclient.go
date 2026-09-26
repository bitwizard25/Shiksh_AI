package gateway

import (
	"net/http"
	"time"
)

// NewHTTPClient returns the client all provider gateways share, so they share one connection
// pool that Warmer keeps warm. Per-call deadlines come from the caller's context; the client
// timeout is only a backstop.
func NewHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 32
	t.IdleConnTimeout = 90 * time.Second
	t.TLSHandshakeTimeout = 5 * time.Second
	t.ForceAttemptHTTP2 = true
	return &http.Client{Transport: t, Timeout: 60 * time.Second}
}
