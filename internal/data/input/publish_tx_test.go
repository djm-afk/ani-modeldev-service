package input_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// A dataset publication must be able to write the READY proof in a transaction
// it owns, so the caller's rollback leaves nothing behind. Two separate public
// methods each committing on their own could not offer that.
func TestPublishVerifiedAlpacaInTxRollsBackWithTheCaller(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	request, contents := alpacaFixture(t)
	repository := input.New(pool)

	if _, err := repository.FreezeImport(ctx, request); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	verified := alpacaProofFor(t, request, contents)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PublishVerifiedAlpacaInTx(ctx, tx, request, verified); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("publish in tx: %v", err)
	}
	// The row is visible inside the transaction only.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil {
		t.Fatalf("read after rollback: %v", err)
	}
	if stored.State == biz.InputStateReady {
		t.Fatal("a rolled-back publication left a READY version behind")
	}

	// The same transaction path commits when the caller commits.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PublishVerifiedAlpacaInTx(ctx, tx, request, verified); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("publish in tx (second attempt): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = repository.Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || stored.State != biz.InputStateReady || stored.AlpacaVerification == nil {
		t.Fatalf("committed publication did not leave READY facts: %+v %v", stored, err)
	}
	if stored.AlpacaVerification.PreparedDataset != verified.PreparedDataset {
		t.Fatal("committed publication changed the derived facts")
	}
}

// A READY replay inside another transaction must return the original proof and
// must not rewrite its timestamp.
func TestPublishVerifiedAlpacaInTxReplaysWithoutRewriting(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	request, contents := alpacaFixture(t)
	repository := input.New(pool)
	if _, err := repository.FreezeImport(ctx, request); err != nil {
		t.Fatal(err)
	}
	first := alpacaProofFor(t, request, contents)

	tx, _ := pool.Begin(ctx)
	published, err := repository.PublishVerifiedAlpacaInTx(ctx, tx, request, first)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	later := first
	later.VerifiedAt = first.VerifiedAt.Add(time.Hour)
	tx, _ = pool.Begin(ctx)
	replayed, err := repository.PublishVerifiedAlpacaInTx(ctx, tx, request, later)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("replay rejected: %v", err)
	}
	tx.Rollback(ctx)
	if !replayed.AlpacaVerification.VerifiedAt.Equal(published.AlpacaVerification.VerifiedAt) {
		t.Fatal("replay rewrote the frozen verification time")
	}
}

func TestPublishVerifiedAlpacaInTxRejectsAnotherFormat(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	request, contents := alpacaFixture(t)
	repository := input.New(pool)
	if _, err := repository.FreezeImport(ctx, request); err != nil {
		t.Fatal(err)
	}
	verified := alpacaProofFor(t, request, contents)

	csv := request
	csv.Format = "CSV"
	tx, _ := pool.Begin(ctx)
	defer tx.Rollback(ctx)
	if _, err := repository.PublishVerifiedAlpacaInTx(ctx, tx, csv, verified); !errors.Is(err, biz.ErrInputVerification) {
		t.Fatalf("CSV request accepted an Alpaca proof: %v", err)
	}
}

func TestPublishVerifiedAlpacaInTxRejectsNilTransaction(t *testing.T) {
	request, contents := alpacaFixtureNoDB()
	verified := alpacaProofFor(t, request, contents)
	repository := input.New(nil)
	if _, err := repository.PublishVerifiedAlpacaInTx(context.Background(), nil, request, verified); err == nil {
		t.Fatal("nil transaction accepted a publication")
	}
}

// alpacaFixture is a complete Alpaca array whose derived facts the trusted
// converter produces. The test never hand-writes the derived numbers.
func alpacaFixture(t *testing.T) (biz.InputImport, string) {
	t.Helper()
	request, contents := alpacaFixtureNoDB()
	prepared, err := cpup01Prepared(contents)
	if err != nil {
		t.Fatalf("derive fixture facts: %v", err)
	}
	request.Object.SizeBytes = int64(len(contents))
	request.Object.SHA256 = prepared.SHA256
	request.Scope.ApprovedPrefix = "inputs/" + request.TenantID
	request.Object.Key = request.Scope.ApprovedPrefix + "/data.json"
	return request, contents
}

func alpacaProofFor(t *testing.T, request biz.InputImport, contents string) biz.VerifiedAlpaca {
	t.Helper()
	prepared, err := cpup01Prepared(contents)
	if err != nil {
		t.Fatalf("derive fixture facts: %v", err)
	}
	return biz.VerifiedAlpaca{
		VerifiedObject:  biz.VerifiedObject{Object: request.Object, VerifiedAt: request.RequestedAt.Add(time.Second)},
		SchemaVersion:   biz.AlpacaSchemaVersion,
		RowCount:        prepared.RowCount,
		PreparedDataset: prepared,
	}
}

func alpacaFixtureNoDB() (biz.InputImport, string) {
	version := "version-1"
	contents := `[{"instruction":"a","input":"","output":"b"}]`
	return biz.InputImport{
		Format: "JSON", TenantID: "11111111-1111-4111-8111-111111111111",
		RequestID: "22222222-2222-4222-8222-222222222222", InputVersionID: "33333333-3333-4333-8333-333333333333",
		Actor: "governance:user:7", RequestedAt: time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond),
		Scope: cpup01.StorageScope{StorageConnectionID: "connection", Bucket: "inputs-bucket", ApprovedPrefix: "inputs/tenant", CredentialReference: "input-read"},
		Object: cpup01.FixedObjectRef{
			StorageConnectionID: "connection", Bucket: "inputs-bucket", Key: "inputs/tenant/data.json",
			VersionID: &version, SizeBytes: int64(len(contents)), SHA256: repeat64("a"),
		},
	}, contents
}

// cpup01Prepared runs the real converter so the fixture never hand-writes
// derived facts the importer would have produced differently.
func cpup01Prepared(contents string) (cpup01.PreparedDatasetFacts, error) {
	return objectstore.ConvertAlpacaJSON(strings.NewReader(contents), io.Discard)
}

func repeat64(fill string) string {
	result := ""
	for len(result) < 64 {
		result += fill
	}
	return result[:64]
}
