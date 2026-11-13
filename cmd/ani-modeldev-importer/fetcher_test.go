package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func redirectPair(fromURL, toURL string) (*http.Request, *http.Request) {
	from, _ := http.NewRequest(http.MethodGet, fromURL, nil)
	to, _ := http.NewRequest(http.MethodGet, toURL, nil)
	return from, to
}

// A throttled or failing hub must be reported as a transient source failure, not
// as a missing file. The download response is the first place this is decided,
// so the mapping is asserted here rather than only on the report code.
func TestHTTPFetcherMapsTransientStatusesToUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			fetcher, err := NewHTTPFetcher(server.URL, server.URL, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			fetcher.client = server.Client()
			plan := planFor(t, "0123456789")
			plan.Provider = biz.ProviderHuggingFace
			plan.ResolvedRevision = "abcdef0123456789"
			if _, err := fetcher.Stream(context.Background(), plan); !errors.Is(err, biz.ErrSourceUnavailable) {
				t.Fatalf("%d returned %v, want IMPORT_DEPENDENCY_UNAVAILABLE", status, err)
			}
		})
	}
}

// A 4xx this adapter caused is neither a missing file nor an outage, and a
// gated repository is its own verdict.
func TestHTTPFetcherMapsClientStatuses(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized: biz.ErrSourceAuthRequired,
		http.StatusForbidden:    biz.ErrSourceAuthRequired,
		http.StatusNotFound:     biz.ErrSourceNotFound,
		http.StatusBadRequest:   biz.ErrSourceInvalidInput,
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		fetcher, err := NewHTTPFetcher(server.URL, server.URL, time.Time{})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		fetcher.client = server.Client()
		plan := planFor(t, "0123456789")
		plan.Provider = biz.ProviderHuggingFace
		plan.ResolvedRevision = "abcdef0123456789"
		_, err = fetcher.Stream(context.Background(), plan)
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("%d returned %v, want %v", status, err, want)
		}
	}
}

// A successful download returns the body so the caller can stream it.
func TestHTTPFetcherReturnsTheBodyOnSuccess(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer server.Close()
	fetcher, err := NewHTTPFetcher(server.URL, server.URL, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	fetcher.client = server.Client()
	plan := planFor(t, "0123456789")
	plan.Provider = biz.ProviderHuggingFace
	plan.ResolvedRevision = "abcdef0123456789"
	body, err := fetcher.Stream(context.Background(), plan)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != "0123456789" {
		t.Fatalf("body = %q", raw)
	}
}

// Real datasets are served from a hub's own CDN through a 302. Refusing every
// redirect made every LFS file undownloadable, so a fixed allowlist replaces the
// blanket refusal.
func TestHTTPFetcherAllowsOnlyListedRedirectTargets(t *testing.T) {
	fetcher, err := NewHTTPFetcher("https://hf-mirror.com", "https://www.modelscope.cn", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		from string
		to   string
		ok   bool
	}{
		{"modelscope cdn", "https://www.modelscope.cn/api/v1/datasets/x", "https://cdn-lfs-cn-1.modelscope.cn/prod/lfs-objects/abc", true},
		{"same host", "https://www.modelscope.cn/api/v1/datasets/x", "https://www.modelscope.cn/api/v1/other", true},
		{"hf mirror cdn", "https://hf-mirror.com/datasets/x/resolve/main/a.json", "https://cdn-lfs.hf-mirror.com/x/a.json", true},
		{"unlisted host", "https://www.modelscope.cn/api/v1/datasets/x", "https://evil.example.com/a", false},
		{"scheme downgrade", "https://www.modelscope.cn/api/v1/datasets/x", "http://cdn-lfs-cn-1.modelscope.cn/a", false},
		{"lookalike host", "https://www.modelscope.cn/api/v1/datasets/x", "https://cdn-lfs-cn-1.modelscope.cn.evil.com/a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := redirectPair(tc.from, tc.to)
			err := fetcher.allowedRedirect(from, to)
			if tc.ok && err != nil {
				t.Fatalf("listed target refused: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("unlisted target was allowed")
			}
		})
	}
}

// The allowlist compares exact hosts, so a URL with credentials or a port must
// not slip through as an "allowed" host.
func TestHTTPFetcherRejectsRedirectWithPortOrUserinfo(t *testing.T) {
	fetcher, _ := NewHTTPFetcher("https://hf-mirror.com", "https://www.modelscope.cn", time.Time{})
	for _, target := range []string{
		"https://cdn-lfs-cn-1.modelscope.cn:8443/a",
		"https://user:pass@cdn-lfs-cn-1.modelscope.cn/a",
	} {
		from, to := redirectPair("https://www.modelscope.cn/api/v1/datasets/x", target)
		if err := fetcher.allowedRedirect(from, to); err == nil {
			t.Fatalf("%s was allowed", target)
		}
	}
}
