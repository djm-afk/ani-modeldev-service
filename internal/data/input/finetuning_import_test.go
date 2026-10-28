package input_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestFinetuningImportPersistsRealVerificationAndReplaysFixedObject(t *testing.T) {
	open := postgres.Prepare(t)
	r := importFixture()
	r.Format = "JSON"
	r.RequestedAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	r.Object.Key = "tenant/input/alpaca.json"
	payload := `[ {"instruction":"概括\u4e00", "input":"", "output":" 原文 "}, {"instruction":"请回答", "input":"\\n","output":"答案"} ]`
	r.Object.SizeBytes = int64(len(payload))
	h := sha256.Sum256([]byte(payload))
	r.Object.SHA256 = hex.EncodeToString(h[:])
	var reads atomic.Int64
	repo := input.New(open())
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reads.Add(1)
		frozen, err := repo.Get(req.Context(), r.TenantID, r.InputVersionID)
		if err != nil || frozen.State != biz.InputStateValidating || frozen.AlpacaVerification != nil || !reflect.DeepEqual(frozen.Import, r) || req.URL.Query().Get("versionId") != *r.Object.VersionID {
			t.Error("read did not follow durable fixation of the original JSON request")
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("x-amz-version-id", *r.Object.VersionID)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	i := managedTestImporter(repo, managedImportVerifier(server, r.Scope.StorageConnectionID))
	got, err := i.ImportFinetuningDataset(ctx, r)
	if err != nil || got.State != biz.InputStateReady || got.Verification != nil || got.AlpacaVerification == nil || got.AlpacaVerification.RowCount != 2 || got.AlpacaVerification.ValidateFor(r) != nil {
		t.Fatalf("JSON did not become verified READY: %+v %v", got, err)
	}
	reader := input.New(open())
	recovered, err := reader.Get(ctx, r.TenantID, r.InputVersionID)
	if err != nil || !reflect.DeepEqual(got, recovered) {
		t.Fatalf("JSON READY was not durable: %+v %v", recovered, err)
	}
	changed := *got.AlpacaVerification
	changed.RowCount++
	if version, err := reader.RecordVerifiedAlpaca(ctx, r, changed); !errors.Is(err, biz.ErrInputVerification) || !reflect.DeepEqual(version, biz.InputVersion{}) {
		t.Fatal("READY JSON replay accepted another row-count proof", version, err)
	}
	changed = *got.AlpacaVerification
	changed.RowCount = 4294967295
	if version, err := reader.RecordVerifiedAlpaca(ctx, r, changed); !errors.Is(err, biz.ErrInputVerification) || !reflect.DeepEqual(version, biz.InputVersion{}) {
		t.Fatal("JSON proof contradicted its prepared row count", version, err)
	}
	replay, err := managedTestImporter(reader, managedImportVerifier(server, r.Scope.StorageConnectionID)).ImportFinetuningDataset(ctx, r)
	if err != nil || !reflect.DeepEqual(got, replay) || reads.Load() != 1 {
		t.Fatal("replay reread or replaced original bytes")
	}
	other := r
	other.Object.Key = "tenant/input/other.json"
	if _, err := i.ImportFinetuningDataset(ctx, other); !errors.Is(err, biz.ErrInputConflict) || reads.Load() != 1 {
		t.Fatal("same identity changed its fixed JSON object")
	}
	other = r
	other.Format = "CSV"
	if _, err := i.ImportCSV(ctx, other); !errors.Is(err, biz.ErrInputConflict) || reads.Load() != 1 {
		t.Fatal("same import was reinterpreted as CSV")
	}
	if _, err := reader.Get(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", r.InputVersionID); !errors.Is(err, biz.ErrInputNotFound) {
		t.Fatal("cross-tenant read exposed JSON import")
	}
}

func TestFinetuningImportRejectsContentDurablyWithoutCreatingReady(t *testing.T) {
	cases := []string{
		`[]`, `{}`, `[{"instruction":"x","input":"","output":"y","output":"z"}]`,
		`[{"instruction":"x","input":"","output":"y","history":[]}]`,
		`[{"instruction":"x","input":null,"output":"y"}]`,
		`[{"instruction":"x","input":2,"output":"y"}]`,
		`[{"instruction":"","input":"","output":"y"}]`,
		`[{"instruction":" ","input":"","output":"y"}]`,
		`[{"instruction":"x","input":""}]`,
		`[{"instruction":"x","input":"","output":"y"}] {}`,
		"\xef\xbb\xbf" + `[{"instruction":"x","input":"","output":"y"}]`,
		`[{"instruction":"x","input":"` + string([]byte{0xff}) + `","output":"y"}]`,
		"instruction,input,output\nx,,y\n",
	}
	for _, payload := range cases {
		t.Run(strings.ReplaceAll(payload[:min(len(payload), 30)], "\n", " "), func(t *testing.T) {
			open := postgres.Prepare(t)
			repo := input.New(open())
			r := importFixture()
			r.Format = "JSON"
			r.Object.Key = "tenant/input/bad.json"
			r.RequestedAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
			r.Object.SizeBytes = int64(len(payload))
			h := sha256.Sum256([]byte(payload))
			r.Object.SHA256 = hex.EncodeToString(h[:])
			var reads atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				reads.Add(1)
				w.Header().Set("x-amz-version-id", *r.Object.VersionID)
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			i := managedTestImporter(repo, managedImportVerifier(server, r.Scope.StorageConnectionID))
			got, err := i.ImportFinetuningDataset(context.Background(), r)
			if !errors.Is(err, biz.ErrInputVerification) || got.State != biz.InputStateRejected || got.AlpacaVerification != nil {
				t.Fatalf("invalid content was not rejected: %+v %v", got, err)
			}
			replay, err := i.ImportFinetuningDataset(context.Background(), r)
			if !errors.Is(err, biz.ErrInputVerification) || !reflect.DeepEqual(got, replay) || reads.Load() != 1 {
				t.Fatal("rejected JSON was retried or promoted")
			}
		})
	}
}
