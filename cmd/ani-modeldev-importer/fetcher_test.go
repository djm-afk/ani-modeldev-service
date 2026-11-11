package main

import (
	"net/http"
	"testing"
	"time"
)

func redirectPair(fromURL, toURL string) (*http.Request, *http.Request) {
	from, _ := http.NewRequest(http.MethodGet, fromURL, nil)
	to, _ := http.NewRequest(http.MethodGet, toURL, nil)
	return from, to
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
