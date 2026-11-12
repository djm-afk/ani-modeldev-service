package datasetprovider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/datasetprovider"
)

const (
	testRepo = "example/support-alpaca"
	testSHA  = "abcdef0123456789abcdef0123456789abcdef01"
)

func newClient(t *testing.T, handler http.HandlerFunc) (*datasetprovider.Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := datasetprovider.New(datasetprovider.Config{
		HuggingFaceEndpoint: server.URL, ModelScopeEndpoint: server.URL, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, server
}

// useTestTransport lets the httptest TLS server be trusted by the real client.
func useTestTransport(client *datasetprovider.Client, server *httptest.Server) {
	client.UseHTTPClient(server.Client())
}

func TestInspectRejectsTwoPathInterpretations(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {})
	useTestTransport(client, server)
	for _, path := range []string{"", "/abs.json", "a/../b.json", "a//b.json", "a\\b.json", "a/b.txt", "a/\x00.json"} {
		if _, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", path); err == nil {
			t.Fatalf("%q accepted", path)
		}
	}
}

func TestInspectHuggingFacePinsCommit(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("client attached credentials: %q", got)
		}
		switch {
		case strings.Contains(r.URL.Path, "/revision/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sha":"` + testSHA + `"}`))
		case strings.Contains(r.URL.Path, "/paths-info/"):
			// The exact-path lookup must run against the resolved commit, not the
			// branch the caller named.
			if !strings.HasSuffix(r.URL.Path, "/paths-info/"+testSHA) {
				t.Errorf("paths-info used %q, want the resolved commit", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"type":"file","path":"alpaca.json","size":4096}]`))
		default:
			http.NotFound(w, r)
		}
	})
	useTestTransport(client, server)
	fact, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if fact.LockKind != biz.LockImmutableRevision || fact.ResolvedRevision != testSHA {
		t.Fatalf("lock = %+v", fact)
	}
	if fact.SizeBytes != 4096 {
		t.Fatalf("size = %d", fact.SizeBytes)
	}
	// A git blob id must never become a file SHA.
	if fact.FileSHA256 != "" {
		t.Fatalf("blob id leaked into the file SHA: %q", fact.FileSHA256)
	}
}

// A mirror without paths-info must still resolve the exact path, and the
// fallback must accept the array shape the tree endpoint returns.
func TestInspectHuggingFaceFallsBackToTreeArray(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/revision/"):
			_, _ = w.Write([]byte(`{"sha":"` + testSHA + `"}`))
		case strings.Contains(r.URL.Path, "/paths-info/"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/tree/"):
			if !strings.Contains(r.URL.Path, testSHA) {
				t.Errorf("tree fallback used %q, want the resolved commit", r.URL.Path)
			}
			_, _ = w.Write([]byte(`[{"type":"file","path":"alpaca.json","size":8192}]`))
		default:
			http.NotFound(w, r)
		}
	})
	useTestTransport(client, server)
	fact, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if fact.SizeBytes != 8192 || fact.ResolvedRevision != testSHA {
		t.Fatalf("fact = %+v", fact)
	}
}

// A listing that answers with a sibling or a parent directory is not evidence
// about the requested file.
func TestInspectHuggingFaceRequiresAnExactPathMatch(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/revision/"):
			_, _ = w.Write([]byte(`{"sha":"` + testSHA + `"}`))
		case strings.Contains(r.URL.Path, "/paths-info/"):
			_, _ = w.Write([]byte(`[{"type":"file","path":"other.json","size":4096}]`))
		default:
			http.NotFound(w, r)
		}
	})
	useTestTransport(client, server)
	if _, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json"); err != biz.ErrSourceNotFound {
		t.Fatalf("a sibling path was accepted: %v", err)
	}
}

// A throttled or failing hub is not a missing file. Reporting it as
// SOURCE_NOT_FOUND would fail the task permanently instead of backing off.
func TestInspectMapsTransientStatusesToUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		useTestTransport(client, server)
		_, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json")
		if err != biz.ErrSourceUnavailable {
			t.Fatalf("%d returned %v, want IMPORT_DEPENDENCY_UNAVAILABLE", status, err)
		}
	}
}

// A 4xx this adapter caused is neither a missing file nor an outage.
func TestInspectMapsOtherClientErrorsToInvalidInput(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	useTestTransport(client, server)
	if _, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json"); err != biz.ErrSourceInvalidInput {
		t.Fatalf("400 returned %v, want SOURCE_INVALID_INPUT", err)
	}
}

func TestInspectRejectsGatedRepository(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		useTestTransport(client, server)
		_, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json")
		if err != biz.ErrSourceAuthRequired {
			t.Fatalf("%d returned %v", status, err)
		}
	}
}

func TestInspectRejectsUnknownPathAndDirectory(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/revision/"):
			_, _ = w.Write([]byte(`{"sha":"` + testSHA + `"}`))
		case strings.Contains(r.URL.Path, "/paths-info/"):
			_, _ = w.Write([]byte(`[{"type":"directory","path":"alpaca.json"}]`))
		default:
			http.NotFound(w, r)
		}
	})
	useTestTransport(client, server)
	if _, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json"); err != biz.ErrSourceNotFound {
		t.Fatalf("directory accepted: %v", err)
	}
}

func TestInspectRejectsOversizeMetadata(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/revision/") {
			_, _ = w.Write([]byte(`{"sha":"` + testSHA + `"}`))
			return
		}
		_, _ = w.Write([]byte(`[{"type":"file","path":"alpaca.json","size":` + strings.Repeat("1", datasetprovider.MaxMetadataBytes) + `}]`))
	})
	useTestTransport(client, server)
	// Oversize metadata is a hub anomaly, not a missing file.
	if _, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json"); err != biz.ErrSourceUnavailable {
		t.Fatalf("oversize metadata returned %v, want IMPORT_DEPENDENCY_UNAVAILABLE", err)
	}
}

func TestInspectModelScopeFallsBackToFileSHA(t *testing.T) {
	digest := strings.Repeat("c", 64)
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Data":{"Revision":"","Files":[{"Path":"alpaca.json","Size":2048,"Sha256":"` + digest + `","Type":"blob"}]}}`))
	})
	useTestTransport(client, server)
	fact, err := client.InspectFile(context.Background(), biz.ProviderModelScope, testRepo, "master", "alpaca.json")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if fact.LockKind != biz.LockFileSHA256 || fact.FileSHA256 != digest || fact.SizeBytes != 2048 {
		t.Fatalf("fact = %+v", fact)
	}
}

func TestInspectModelScopeWithoutProofIsNotPinnable(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Data":{"Revision":"","Files":[{"Path":"alpaca.json","Size":2048,"Sha256":"","Type":"blob"}]}}`))
	})
	useTestTransport(client, server)
	if _, err := client.InspectFile(context.Background(), biz.ProviderModelScope, testRepo, "master", "alpaca.json"); err != biz.ErrSourceNotPinnable {
		t.Fatalf("unpinnable file accepted: %v", err)
	}
}

func TestInspectRejectsUnknownProvider(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {})
	useTestTransport(client, server)
	if _, err := client.InspectFile(context.Background(), "GITHUB", testRepo, "main", "a.json"); err != biz.ErrSourceInvalidInput {
		t.Fatalf("unknown provider accepted: %v", err)
	}
}

func TestListDirectoryBindsPageToken(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			// A token minted for another repository must never be followed.
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.Header().Set("Link", `<https://hub.example/api/datasets/`+testRepo+`/tree/main?cursor=abc>; rel="next"`)
		_, _ = w.Write([]byte(`[{"type":"file","path":"a.json","size":10}]`))
	})
	useTestTransport(client, server)
	ctx := context.Background()
	if _, err := client.ListDirectory(ctx, biz.ProviderHuggingFace, testRepo, "main", "", "other/repo|main||1", 1); err != biz.ErrSourceInvalidInput {
		t.Fatalf("foreign token accepted: %v", err)
	}
	page, err := client.ListDirectory(ctx, biz.ProviderHuggingFace, testRepo, "main", "", "", 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Entries) != 1 || !page.HasMore || page.NextPageToken == "" {
		t.Fatalf("page = %+v", page)
	}
	// The next request must carry the hub's own cursor, not this adapter's token.
	if _, err := client.ListDirectory(ctx, biz.ProviderHuggingFace, testRepo, "main", "", page.NextPageToken, 1); err != nil {
		t.Fatalf("second page: %v", err)
	}
}

// A full page without a Link header is the last page. Inventing a cursor from
// the entry count made the second request repeat the first page forever.
func TestListDirectoryDoesNotInventACursor(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"type":"file","path":"a.json","size":10}]`))
	})
	useTestTransport(client, server)
	page, err := client.ListDirectory(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "", "", 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.HasMore || page.NextPageToken != "" {
		t.Fatalf("a full page without a Link header claimed more: %+v", page)
	}
}

// ModelScope returns the whole tree at once, so the second page must start
// after the entries the first page already returned.
func TestListModelScopePagesByOffset(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Data":{"Revision":"","Files":[` +
			`{"Path":"a.json","Size":1,"Type":"blob"},` +
			`{"Path":"b.json","Size":2,"Type":"blob"},` +
			`{"Path":"c.json","Size":3,"Type":"blob"}]}}`))
	})
	useTestTransport(client, server)
	ctx := context.Background()
	first, err := client.ListDirectory(ctx, biz.ProviderModelScope, testRepo, "master", "", "", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Entries) != 2 || !first.HasMore || first.NextPageToken == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := client.ListDirectory(ctx, biz.ProviderModelScope, testRepo, "master", "", first.NextPageToken, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Entries) != 1 || second.Entries[0].Path != "c.json" || second.HasMore {
		t.Fatalf("second page = %+v", second)
	}
}

func TestListDirectoryBoundsPageSize(t *testing.T) {
	client, server := newClient(t, func(w http.ResponseWriter, r *http.Request) {})
	useTestTransport(client, server)
	ctx := context.Background()
	for _, size := range []int32{-1, datasetprovider.MaxPageSize + 1, 10000} {
		_, err := client.ListDirectory(ctx, biz.ProviderHuggingFace, testRepo, "main", "", "", size)
		if size > datasetprovider.MaxPageSize && err != biz.ErrSourceInvalidInput {
			t.Fatalf("size %d accepted: %v", size, err)
		}
	}
}

func TestNewClientRequiresHTTPSOrigins(t *testing.T) {
	for _, config := range []datasetprovider.Config{
		{},
		{HuggingFaceEndpoint: "http://insecure.example", ModelScopeEndpoint: "https://ok.example"},
		{HuggingFaceEndpoint: "https://ok.example", ModelScopeEndpoint: "https://ok.example?x=1"},
	} {
		if _, err := datasetprovider.New(config); err == nil {
			t.Fatalf("config %+v accepted", config)
		}
	}
}
