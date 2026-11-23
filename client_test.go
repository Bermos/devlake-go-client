package devlake_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	devlake "github.com/Bermos/devlake-go-client"
)

func TestNewRejectsBadBaseURL(t *testing.T) {
	for _, u := range []string{"", "devlake:8080", "ftp://devlake", "://bad"} {
		if _, err := devlake.New(u); err == nil {
			t.Errorf("New(%q) succeeded, want error", u)
		}
	}
}

func TestNewRejectsNilOptions(t *testing.T) {
	if _, err := devlake.New("http://x", devlake.WithHTTPClient(nil)); err == nil {
		t.Error("WithHTTPClient(nil) accepted")
	}
	if _, err := devlake.New("http://x", devlake.WithLogger(nil)); err == nil {
		t.Error("WithLogger(nil) accepted")
	}
}

// recordingDoer proves that an injected HTTP client is used for requests.
type recordingDoer struct {
	reqs []*http.Request
	next http.Client
}

func (d *recordingDoer) Do(r *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, r)
	return d.next.Do(r)
}

func TestOptionsAreApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/rest/version" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "my-app/1.0" {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("X-Extra"); got != "yes" {
			t.Errorf("X-Extra = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v1.0.3-beta19@abc"}`))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	doer := &recordingDoer{}
	c, err := devlake.New(srv.URL+"/api/rest/",
		devlake.WithHTTPClient(doer),
		devlake.WithAPIKey("secret"),
		devlake.WithUserAgent("my-app/1.0"),
		devlake.WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		devlake.WithRequestEditor(func(_ context.Context, r *http.Request) error {
			r.Header.Set("X-Extra", "yes")
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "v1.0.3-beta19@abc" {
		t.Errorf("Version = %q", v)
	}
	if len(doer.reqs) != 1 {
		t.Errorf("injected client saw %d requests, want 1", len(doer.reqs))
	}
	if !strings.Contains(logs.String(), "devlake request") || !strings.Contains(logs.String(), "status=200") {
		t.Errorf("request was not logged: %s", logs.String())
	}
}

func TestAPIErrorDecoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"message":"blueprint not found","causes":["record not found"]}`))
	}))
	defer srv.Close()

	c, err := devlake.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Ping(context.Background())
	var apiErr *devlake.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 404 || apiErr.Message != "blueprint not found" || len(apiErr.Causes) != 1 {
		t.Errorf("unexpected error contents: %+v", apiErr)
	}
	if !errors.Is(err, devlake.ErrNotFound) {
		t.Error("errors.Is(err, ErrNotFound) = false")
	}
}
