// Package devlake is a Go client for Apache DevLake.
//
// The package wraps the generated low-level client in the devlakeapi package
// and adds dependency injection friendly construction, logging, auth and
// error handling. Every endpoint DevLake documents is reachable via
// [Client.API]; convenience helpers for common workflows live on [Client].
//
// Base URL: when talking to DevLake through config-ui use
// "https://devlake.example.com/api". When talking to the backend directly use
// "http://devlake:8080". When authenticating with an API key, DevLake serves
// the API under the "/rest" prefix, e.g. "https://devlake.example.com/api/rest".
package devlake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Bermos/devlake-go-client/devlakeapi"
)

// HTTPDoer is the minimal interface the client needs to send requests.
// *http.Client satisfies it, as do instrumented clients (OpenTelemetry,
// retrying clients, test doubles, ...).
type HTTPDoer = devlakeapi.HttpRequestDoer

// RequestEditorFn can mutate every outgoing request before it is sent.
type RequestEditorFn = devlakeapi.RequestEditorFn

// Client talks to a single DevLake instance. It is safe for concurrent use.
type Client struct {
	baseURL string
	api     *devlakeapi.ClientWithResponses
	logger  *slog.Logger
	poll    time.Duration
	doer    HTTPDoer
	editors []RequestEditorFn
}

type config struct {
	httpClient HTTPDoer
	logger     *slog.Logger
	editors    []RequestEditorFn
	userAgent  string
	poll       time.Duration
}

// Option configures a [Client].
type Option func(*config) error

// WithHTTPClient sets the HTTP client used for all requests. Use it to inject
// an instrumented client, custom transports, proxies or TLS settings.
// Defaults to an *http.Client with a 60 second timeout.
func WithHTTPClient(c HTTPDoer) Option {
	return func(cfg *config) error {
		if c == nil {
			return errors.New("devlake: nil HTTP client")
		}
		cfg.httpClient = c
		return nil
	}
}

// WithLogger sets the structured logger. Requests are logged at debug level,
// failures at warn level. Defaults to a logger that discards everything.
func WithLogger(l *slog.Logger) Option {
	return func(cfg *config) error {
		if l == nil {
			return errors.New("devlake: nil logger")
		}
		cfg.logger = l
		return nil
	}
}

// WithAPIKey authenticates with a DevLake API key (sent as a Bearer token).
// DevLake only checks API keys on the "/rest" prefix, so the base URL should
// end in "/rest".
func WithAPIKey(key string) Option {
	return WithRequestEditor(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+key)
		return nil
	})
}

// WithBasicAuth authenticates with HTTP basic auth, as used when config-ui is
// protected by ADMIN_USER / ADMIN_PASS.
func WithBasicAuth(user, pass string) Option {
	return WithRequestEditor(func(_ context.Context, req *http.Request) error {
		req.SetBasicAuth(user, pass)
		return nil
	})
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(cfg *config) error {
		cfg.userAgent = ua
		return nil
	}
}

// WithRequestEditor adds a function that may modify every request, e.g. to
// set custom headers for an auth proxy.
func WithRequestEditor(fn RequestEditorFn) Option {
	return func(cfg *config) error {
		if fn == nil {
			return errors.New("devlake: nil request editor")
		}
		cfg.editors = append(cfg.editors, fn)
		return nil
	}
}

// WithPollInterval sets how often wait helpers poll DevLake. Defaults to 5s.
func WithPollInterval(d time.Duration) Option {
	return func(cfg *config) error {
		if d <= 0 {
			return errors.New("devlake: poll interval must be positive")
		}
		cfg.poll = d
		return nil
	}
}

// DefaultUserAgent is sent unless [WithUserAgent] is used.
const DefaultUserAgent = "devlake-go-client"

// New creates a client for the DevLake instance at baseURL.
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("devlake: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("devlake: base URL must be http or https, got %q", baseURL)
	}
	cfg := &config{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		logger:     slog.New(slog.DiscardHandler),
		userAgent:  DefaultUserAgent,
		poll:       5 * time.Second,
	}
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	base := strings.TrimRight(baseURL, "/")
	ua := cfg.userAgent
	editors := append([]RequestEditorFn{func(_ context.Context, req *http.Request) error {
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		if req.Header.Get("Accept") == "" {
			req.Header.Set("Accept", "application/json")
		}
		return nil
	}}, cfg.editors...)

	doer := &loggingDoer{next: cfg.httpClient, logger: cfg.logger}
	apiOpts := []devlakeapi.ClientOption{devlakeapi.WithHTTPClient(doer)}
	for _, e := range editors {
		apiOpts = append(apiOpts, devlakeapi.WithRequestEditorFn(e))
	}
	api, err := devlakeapi.NewClientWithResponses(base, apiOpts...)
	if err != nil {
		return nil, fmt.Errorf("devlake: %w", err)
	}
	return &Client{
		baseURL: base,
		api:     api,
		logger:  cfg.logger,
		poll:    cfg.poll,
		doer:    doer,
		editors: editors,
	}, nil
}

// API returns the generated low-level client, which covers every endpoint in
// DevLake's OpenAPI document, including all plugins.
func (c *Client) API() *devlakeapi.ClientWithResponses { return c.api }

// BaseURL returns the normalized base URL the client sends requests to.
func (c *Client) BaseURL() string { return c.baseURL }

// loggingDoer logs every request and its outcome.
type loggingDoer struct {
	next   HTTPDoer
	logger *slog.Logger
}

func (d *loggingDoer) Do(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := d.next.Do(req)
	attrs := []any{
		slog.String("method", req.Method),
		slog.String("url", redactURL(req.URL)),
		slog.Duration("duration", time.Since(start)),
	}
	if err != nil {
		d.logger.WarnContext(req.Context(), "devlake request failed", append(attrs, slog.Any("error", err))...)
		return resp, err
	}
	attrs = append(attrs, slog.Int("status", resp.StatusCode))
	if resp.StatusCode >= 400 {
		d.logger.WarnContext(req.Context(), "devlake request returned error status", attrs...)
	} else {
		d.logger.DebugContext(req.Context(), "devlake request", attrs...)
	}
	return resp, nil
}

func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	return c.String()
}
