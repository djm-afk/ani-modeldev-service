package input_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestInputVerificationRequiresTheCompleteAlreadyFrozenRequest(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository := input.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	original := importFixture()
	original.Scope.CredentialReference = "managed-secret:original-input-reader"
	if got, err := repository.RecordVerifiedCSV(ctx, original, verificationObservation(original)); !errors.Is(err, biz.ErrInputNotFound) || !reflect.DeepEqual(got, biz.InputVersion{}) {
		t.Fatalf("verification without a frozen import produced a receipt: %+v, %v", got, err)
	}
	requireInputAbsent(t, ctx, repository, original.TenantID, original.InputVersionID)
	frozen, err := repository.FreezeImport(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		change    func(*biz.InputImport)
		wantError error
	}{
		{"missing input", func(c *biz.InputImport) { c.InputVersionID = "44444444-4444-4444-8444-444444444444" }, biz.ErrInputNotFound},
		{"wrong tenant", func(c *biz.InputImport) { c.TenantID = "88888888-8888-4888-8888-888888888888" }, biz.ErrInputNotFound},
		{"wrong request", func(c *biz.InputImport) { c.RequestID = "55555555-5555-4555-8555-555555555555" }, biz.ErrInputConflict},
		{"audit actor", func(c *biz.InputImport) { c.Actor = "governance:user:43" }, biz.ErrInputConflict},
		{"audit timestamp", func(c *biz.InputImport) { c.RequestedAt = c.RequestedAt.Add(time.Microsecond) }, biz.ErrInputConflict},
		{"storage connection", func(c *biz.InputImport) {
			c.Scope.StorageConnectionID = "another-input-connection"
			c.Object.StorageConnectionID = c.Scope.StorageConnectionID
		}, biz.ErrInputConflict},
		{"bucket", func(c *biz.InputImport) { c.Scope.Bucket = "another-input-bucket"; c.Object.Bucket = c.Scope.Bucket }, biz.ErrInputConflict},
		{"approved prefix", func(c *biz.InputImport) { c.Scope.ApprovedPrefix = "tenant" }, biz.ErrInputConflict},
		{"credential reference replaced", func(c *biz.InputImport) { c.Scope.CredentialReference = "managed-secret:other-reader" }, biz.ErrInputConflict},
		{"credential reference removed", func(c *biz.InputImport) { c.Scope.CredentialReference = "" }, biz.ErrInputConflict},
		{"object key", func(c *biz.InputImport) { c.Object.Key = "tenant/input/other.csv" }, biz.ErrInputConflict},
		{"object version", func(c *biz.InputImport) { version := "fixture-source-version-2"; c.Object.VersionID = &version }, biz.ErrInputConflict},
		{"size", func(c *biz.InputImport) { c.Object.SizeBytes++ }, biz.ErrInputConflict},
		{"digest", func(c *biz.InputImport) { c.Object.SHA256 = strings.Repeat("b", 64) }, biz.ErrInputConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := original
			test.change(&candidate)
			if err := candidate.Validate(); err != nil {
				t.Fatalf("changed request must remain structurally valid: %v", err)
			}
			// Match the changed command so proof validation alone cannot satisfy
			// this test: persistence must compare the original frozen request.
			got, err := repository.RecordVerifiedCSV(ctx, candidate, verificationObservation(candidate))
			if !errors.Is(err, test.wantError) || !reflect.DeepEqual(got, biz.InputVersion{}) {
				t.Fatalf("verification changed the command fixed by another import: %+v, %v", got, err)
			}
			requireStoredInputVersion(t, ctx, repository, frozen)
			if errors.Is(test.wantError, biz.ErrInputNotFound) {
				requireInputAbsent(t, ctx, repository, candidate.TenantID, candidate.InputVersionID)
			}
		})
	}
	if _, err := repository.RecordVerifiedCSV(ctx, original, verificationObservation(original)); err != nil {
		t.Fatalf("rejected substitutions poisoned the original verification: %v", err)
	}
}

func TestInputVerificationInvalidProofsCannotChangeValidatingOrReadyVersions(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository := input.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := importFixture()
	frozen, err := repository.FreezeImport(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	invalidProofs := []struct {
		name   string
		change func(*biz.VerifiedCSV)
	}{
		{"storage connection", func(p *biz.VerifiedCSV) { p.Object.StorageConnectionID = "other-connection" }},
		{"bucket", func(p *biz.VerifiedCSV) { p.Object.Bucket = "other-bucket" }},
		{"key", func(p *biz.VerifiedCSV) { p.Object.Key = "tenant/input/other.csv" }},
		{"version", func(p *biz.VerifiedCSV) { version := "fixture-source-version-2"; p.Object.VersionID = &version }},
		{"missing version", func(p *biz.VerifiedCSV) { p.Object.VersionID = nil }},
		{"immutable copy true", func(p *biz.VerifiedCSV) { copy := true; p.Object.ImmutableCopy = &copy }},
		{"immutable copy false presence", func(p *biz.VerifiedCSV) { copy := false; p.Object.ImmutableCopy = &copy }},
		{"size", func(p *biz.VerifiedCSV) { p.Object.SizeBytes++ }},
		{"digest", func(p *biz.VerifiedCSV) { p.Object.SHA256 = strings.Repeat("b", 64) }},
		{"schema", func(p *biz.VerifiedCSV) { p.SchemaVersion = "ani.cpu.csv.v2" }},
		{"rows", func(p *biz.VerifiedCSV) { p.RowCount = 0 }},
		{"features", func(p *biz.VerifiedCSV) { p.FeatureCount = 15 }},
		{"row count overflow", func(p *biz.VerifiedCSV) { p.RowCount = 4294967295 }},
		{"missing time", func(p *biz.VerifiedCSV) { p.VerifiedAt = time.Time{} }},
		{"before import", func(p *biz.VerifiedCSV) { p.VerifiedAt = request.RequestedAt.Add(-time.Nanosecond) }},
		{"time outside supported year", func(p *biz.VerifiedCSV) { p.VerifiedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
	}
	unchanged := frozen
	for _, state := range []string{"VALIDATING", "READY"} {
		if state == "READY" {
			unchanged, err = repository.RecordVerifiedCSV(ctx, request, verificationObservation(request))
			if err != nil {
				t.Fatalf("valid proof after rejections: %v", err)
			}
		}
		t.Run(state, func(t *testing.T) {
			for _, test := range invalidProofs {
				t.Run(test.name, func(t *testing.T) {
					proof := verificationObservation(request)
					test.change(&proof)
					got, err := repository.RecordVerifiedCSV(ctx, request, proof)
					if !errors.Is(err, biz.ErrInputVerification) || !reflect.DeepEqual(got, biz.InputVersion{}) {
						t.Fatalf("invalid proof was accepted: %+v, %v", got, err)
					}
					requireStoredInputVersion(t, ctx, repository, unchanged)
				})
			}
		})
	}
	requireStoredInputVersion(t, ctx, input.New(openPool()), unchanged)
}

func TestInputVerificationEquivalentRechecksKeepFirstProofAndImportReplayCannotDowngradeReady(t *testing.T) {
	openPool := postgres.Prepare(t)
	writer := openPool()
	repository := input.New(writer)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := importFixture()
	if _, err := repository.FreezeImport(ctx, request); err != nil {
		t.Fatal(err)
	}
	firstProof := verificationObservation(request)
	first, err := repository.RecordVerifiedCSV(ctx, request, firstProof)
	if err != nil || !reflect.DeepEqual(first, biz.InputVersion{Import: request, State: biz.InputStateReady, Verification: &firstProof}) {
		t.Fatalf("first durable proof differed: %+v, %v", first, err)
	}
	writer.Close()
	repository = input.New(openPool())
	for _, observedAt := range []time.Time{request.RequestedAt.Add(2 * time.Minute), request.RequestedAt} {
		proof := verificationObservation(request)
		proof.VerifiedAt = observedAt
		got, err := repository.RecordVerifiedCSV(ctx, request, proof)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatalf("equivalent recheck rewrote the first durable proof: %+v, %v", got, err)
		}
		got, err = repository.FreezeImport(ctx, request)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatalf("import replay downgraded READY or changed proof: %+v, %v", got, err)
		}
		requireStoredInputVersion(t, ctx, repository, first)
	}
	changed := verificationObservation(request)
	changed.RowCount--
	if got, err := repository.RecordVerifiedCSV(ctx, request, changed); !errors.Is(err, biz.ErrInputVerification) || !reflect.DeepEqual(got, biz.InputVersion{}) {
		t.Fatal("READY replay accepted another structurally valid row-count proof", got, err)
	}
	requireStoredInputVersion(t, ctx, repository, first)
}

func TestInputVerificationStoresMicrosecondUTCWithoutRoundingOrShiftingTheObservation(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository := input.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := importFixture()
	if _, err := repository.FreezeImport(ctx, request); err != nil {
		t.Fatal(err)
	}
	proof := verificationObservation(request)
	proof.VerifiedAt = time.Date(2026, 9, 30, 20, 1, 0, 123987, time.FixedZone("fixture-UTC+8", 8*60*60))
	// Independent literal: 20:01 at +08:00 is 12:01 UTC, with the
	// sub-microsecond fraction dropped, not rounded to the next microsecond.
	wantProof := verificationObservation(request)
	wantProof.VerifiedAt = time.Date(2026, 9, 30, 12, 1, 0, 123000, time.UTC)
	want := biz.InputVersion{Import: request, State: biz.InputStateReady, Verification: &wantProof}
	got, err := repository.RecordVerifiedCSV(ctx, request, proof)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("verification timestamp was not stored in the fixed microsecond UTC form: %+v, %v", got, err)
	}
	requireStoredInputVersion(t, ctx, input.New(openPool()), want)
}

func TestConcurrentInputVerificationAcrossIndependentPoolsConvergesOnTheFirstDurableProof(t *testing.T) {
	openPool := postgres.Prepare(t)
	repositories := [2]*input.Repository{input.New(openPool()), input.New(openPool())}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := importFixture()
	if _, err := repositories[0].FreezeImport(ctx, request); err != nil {
		t.Fatal(err)
	}
	proofs := [2]biz.VerifiedCSV{verificationObservation(request), verificationObservation(request)}
	proofs[1].VerifiedAt = request.RequestedAt.Add(2 * time.Minute)
	start := make(chan struct{})
	type result struct {
		version biz.InputVersion
		err     error
	}
	completed := make(chan result, 2)
	for i := range repositories {
		go func(index int) {
			<-start
			version, err := repositories[index].RecordVerifiedCSV(ctx, request, proofs[index])
			completed <- result{version: version, err: err}
		}(i)
	}
	close(start)
	results := [2]result{<-completed, <-completed}
	for _, got := range results {
		if got.err != nil || got.version.State != biz.InputStateReady || !reflect.DeepEqual(got.version.Import, request) || got.version.Verification == nil {
			t.Fatalf("concurrent verification lost the frozen import or READY proof: %+v, %v", got.version, got.err)
		}
		if !reflect.DeepEqual(*got.version.Verification, proofs[0]) && !reflect.DeepEqual(*got.version.Verification, proofs[1]) {
			t.Fatalf("concurrent verification invented or mixed proof facts: %+v", got.version.Verification)
		}
	}
	first := results[0].version
	if !reflect.DeepEqual(results[1].version, first) {
		t.Fatalf("concurrent callers received different durable proofs: %+v / %+v", first.Verification, results[1].version.Verification)
	}
	repository := input.New(openPool())
	for _, proof := range proofs {
		got, err := repository.RecordVerifiedCSV(ctx, request, proof)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatalf("retry changed the concurrent winner: %+v, %v", got, err)
		}
	}
	if got, err := repository.FreezeImport(ctx, request); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("import replay changed the concurrent winner: %+v, %v", got, err)
	}
	requireStoredInputVersion(t, ctx, repository, first)
}

// This is synthetic verifier output for repository-only durability tests. It
// does not claim that any actual S3 object or CSV bytes have been observed.
func verificationObservation(request biz.InputImport) biz.VerifiedCSV {
	return biz.VerifiedCSV{
		VerifiedObject: biz.VerifiedObject{Object: request.Object, VerifiedAt: request.RequestedAt.Add(time.Minute)},
		SchemaVersion:  "ani.cpu.csv.v1", RowCount: 1024, FeatureCount: 16,
	}
}

func requireStoredInputVersion(t *testing.T, ctx context.Context, repository *input.Repository, want biz.InputVersion) {
	t.Helper()
	got, err := repository.Get(ctx, want.Import.TenantID, want.Import.InputVersionID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected or repeated verification changed durable input facts: %+v, %v", got, err)
	}
}
