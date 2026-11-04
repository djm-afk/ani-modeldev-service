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
		case strings.Contains(r.URL.Path, "/tree/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"type":"file","path":"alpaca.json","size":4096}`))
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
		case strings.Contains(r.URL.Path, "/tree/"):
			_, _ = w.Write([]byte(`{"type":"directory","path":"alpaca.json"}`))
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
		_, _ = w.Write([]byte(`{"type":"file","path":"alpaca.json","size":` + strings.Repeat("1", datasetprovider.MaxMetadataBytes) + `}`))
	})
	useTestTransport(client, server)
	if _, err := client.InspectFile(context.Background(), biz.ProviderHuggingFace, testRepo, "main", "alpaca.json"); err == nil {
		t.Fatal("oversize metadata accepted")
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
