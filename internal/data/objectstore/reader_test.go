package objectstore_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

func TestReadObjectReturnsExactVersionBytes(t *testing.T) {
	payload := `{"schema":"ani.metric.v1","step":1,"name":"train.loss","value":0.5}` + "\n"
	scope, object := objectFixture(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/cpu-artifacts/tenant/execution/model.pt" || r.URL.Query().Get("versionId") != "version-1" {
			t.Errorf("unexpected object request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("x-amz-version-id", "version-1")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	data, err := objectstore.NewObjectReader(testS3Client(server), scope.StorageConnectionID).ReadObject(context.Background(), scope, object, 1<<20)
	if err != nil {
		t.Fatalf("read published object: %v", err)
	}
	if string(data) != payload {
		t.Fatalf("payload=%q, want %q", data, payload)
	}
}

func TestReadObjectRejectsOversizedObject(t *testing.T) {
	payload := strings.Repeat("x", 32)
	scope, object := objectFixture(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-amz-version-id", "version-1")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	// A truncated read would still look like a plausible partial series, so the
	// budget overflow must fail instead of returning a prefix.
	data, err := objectstore.NewObjectReader(testS3Client(server), scope.StorageConnectionID).ReadObject(context.Background(), scope, object, 16)
	if !errors.Is(err, biz.ErrObjectSourceUnavailable) || len(data) != 0 {
		t.Fatalf("data=%q err=%v, want a budget rejection", data, err)
	}
}

func TestReadObjectRejectsForeignScopeBeforeNetwork(t *testing.T) {
	scope, object := objectFixture("payload")
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	for _, test := range []struct {
		name  string
		scope cpup01.StorageScope
		limit int64
	}{
		{name: "other connection", scope: cpup01.StorageScope{StorageConnectionID: "other-store", Bucket: scope.Bucket, ApprovedPrefix: scope.ApprovedPrefix}, limit: 1 << 20},
		{name: "key outside prefix", scope: scope, limit: 1 << 20},
		{name: "no budget", scope: scope, limit: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := object
			if test.name == "key outside prefix" {
				key.Key = "elsewhere/model.pt"
			}
			if _, err := objectstore.NewObjectReader(testS3Client(server), test.scope.StorageConnectionID).ReadObject(context.Background(), test.scope, key, test.limit); !errors.Is(err, biz.ErrObjectSourceUnavailable) {
				t.Fatalf("err=%v, want source unavailable", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests=%d, want none", requests.Load())
	}
}

func TestReadObjectReportsUnavailableSource(t *testing.T) {
	scope, object := objectFixture("payload")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	if _, err := objectstore.NewObjectReader(testS3Client(server), scope.StorageConnectionID).ReadObject(context.Background(), scope, object, 1<<20); !errors.Is(err, biz.ErrObjectSourceUnavailable) {
		t.Fatalf("err=%v, want source unavailable", err)
	}
}
