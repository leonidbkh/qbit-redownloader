package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRutrackerForumResolverRetriesTransientServerErrors(t *testing.T) {
	t.Parallel()

	const expectedHash = "B99748278658EA9CB129C21F570C5EE3BAA99D13"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "temporary browser failure", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"status":"ok","solution":{"status":200,"response":"<a href=\"magnet:?xt=urn:btih:%s\">download</a>"}}`, expectedHash)
	}))
	defer server.Close()

	resolver := NewRutrackerForumResolver("https://rutracker.org/forum", server.URL)
	resolver.retryBaseDelay = 0
	resolver.retryJitterLimit = 0
	resolved, err := resolver.Resolve(context.Background(), "6875876")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.InfoHash != "b99748278658ea9cb129c21f570c5ee3baa99d13" {
		t.Fatalf("resolved hash = %q", resolved.InfoHash)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("flaresolverr calls = %d, want 3", got)
	}
}

func TestRutrackerForumResolverStopsAfterTransientRetriesAreExhausted(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "temporary browser failure", http.StatusBadGateway)
	}))
	defer server.Close()

	resolver := NewRutrackerForumResolver("https://rutracker.org/forum", server.URL)
	resolver.retryBaseDelay = 0
	resolver.retryJitterLimit = 0
	_, err := resolver.Resolve(context.Background(), "6875876")
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("Resolve error = %v, want exhausted retry error", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("flaresolverr calls = %d, want 3", got)
	}
}

func TestRutrackerForumResolverDoesNotRetryMissingMagnet(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"status":"ok","solution":{"status":200,"response":"<html>topic removed</html>"}}`)
	}))
	defer server.Close()

	resolver := NewRutrackerForumResolver("https://rutracker.org/forum", server.URL)
	resolver.retryBaseDelay = 0
	resolver.retryJitterLimit = 0
	_, err := resolver.Resolve(context.Background(), "6875876")
	if !errors.Is(err, ErrTopicHasNoMagnet) {
		t.Fatalf("Resolve error = %v, want ErrTopicHasNoMagnet", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("flaresolverr calls = %d, want 1", got)
	}
}
