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

func TestFreezeImportConflictingIdentifiersPreserveBothOriginalVersions(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository := input.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := importFixture()
	second := importFixture()
	second.RequestID = "44444444-4444-4444-8444-444444444444"
	second.InputVersionID = "55555555-5555-4555-8555-555555555555"
	second.Object.Key = "tenant/input/second.csv"
	for _, original := range []biz.InputImport{first, second} {
		if _, err := repository.FreezeImport(ctx, original); err != nil {
			t.Fatalf("initial fixed import: %v", err)
		}
	}
	for _, test := range []struct {
		name               string
		requestID, inputID string
	}{
		{"input already owned by another request", "66666666-6666-4666-8666-666666666666", first.InputVersionID},
		{"request already owns another input", first.RequestID, "77777777-7777-4777-8777-777777777777"},
		{"crossed existing identities", first.RequestID, second.InputVersionID},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := importFixture()
			candidate.RequestID, candidate.InputVersionID = test.requestID, test.inputID
			for range 2 {
				got, err := repository.FreezeImport(ctx, candidate)
				if !errors.Is(err, biz.ErrInputConflict) || !reflect.DeepEqual(got, biz.InputVersion{}) {
					t.Fatalf("identifier collision did not remain a conflict: %+v, %v", got, err)
				}
			}
			for _, original := range []biz.InputImport{first, second} {
				requireFrozenImport(t, ctx, repository, original)
			}
			if candidate.InputVersionID != first.InputVersionID && candidate.InputVersionID != second.InputVersionID {
				requireInputAbsent(t, ctx, repository, candidate.TenantID, candidate.InputVersionID)
			}
		})
	}
}

func TestFreezeImportRejectsEveryChangedFrozenFactWithoutRewritingTheOriginal(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository := input.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	original := importFixture()
	original.Scope.CredentialReference = "managed-secret:original-input-reader"
	if _, err := repository.FreezeImport(ctx, original); err != nil {
		t.Fatalf("initial fixed import: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*biz.InputImport)
	}{
		{"audit actor", func(c *biz.InputImport) { c.Actor = "governance:user:43" }},
		{"audit timestamp", func(c *biz.InputImport) { c.RequestedAt = c.RequestedAt.Add(time.Microsecond) }},
		{"storage connection", func(c *biz.InputImport) {
			c.Scope.StorageConnectionID = "another-input-connection"
			c.Object.StorageConnectionID = c.Scope.StorageConnectionID
		}},
		{"bucket", func(c *biz.InputImport) { c.Scope.Bucket = "another-input-bucket"; c.Object.Bucket = c.Scope.Bucket }},
		{"approved prefix", func(c *biz.InputImport) { c.Scope.ApprovedPrefix = "tenant" }},
		{"credential reference replaced", func(c *biz.InputImport) { c.Scope.CredentialReference = "managed-secret:other-input-reader" }},
		{"credential reference removed", func(c *biz.InputImport) { c.Scope.CredentialReference = "" }},
		{"object key", func(c *biz.InputImport) { c.Object.Key = "tenant/input/other.csv" }},
		{"object version", func(c *biz.InputImport) { version := "fixture-source-version-2"; c.Object.VersionID = &version }},
		{"size", func(c *biz.InputImport) { c.Object.SizeBytes++ }},
		{"content digest", func(c *biz.InputImport) { c.Object.SHA256 = strings.Repeat("b", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := original
			test.change(&candidate)
			if err := candidate.Validate(); err != nil {
				t.Fatalf("changed-fact fixture must remain a valid request: %v", err)
			}
			for range 2 {
				got, err := repository.FreezeImport(ctx, candidate)
				if !errors.Is(err, biz.ErrInputConflict) || !reflect.DeepEqual(got, biz.InputVersion{}) {
					t.Fatalf("changed frozen fact was acknowledged: %+v, %v", got, err)
				}
			}
			requireFrozenImport(t, ctx, repository, original)
		})
	}
	// A rejected change must not poison a later retry of the original request.
	if got, err := repository.FreezeImport(ctx, original); err != nil || !reflect.DeepEqual(got, biz.InputVersion{Import: original, State: biz.InputStateValidating}) {
		t.Fatalf("original retry after conflicts changed: %+v, %v", got, err)
	}
}

func TestFreezeImportScopesIdenticalRequestAndInputIDsToTheirTenant(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository := input.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := importFixture()
	second := importFixture()
	second.TenantID = "88888888-8888-4888-8888-888888888888"
	second.Actor = "governance:user:84"
	second.Object.Key = "tenant/input/tenant-two.csv"
	if _, err := repository.FreezeImport(ctx, first); err != nil {
		t.Fatal(err)
	}
	requireInputAbsent(t, ctx, repository, second.TenantID, first.InputVersionID)
	if _, err := repository.FreezeImport(ctx, second); err != nil {
		t.Fatalf("another tenant could not reuse its own request/input UUIDs: %v", err)
	}
	reader := input.New(openPool())
	requireFrozenImport(t, ctx, reader, first)
	requireFrozenImport(t, ctx, reader, second)
	requireInputAbsent(t, ctx, reader, "99999999-9999-4999-8999-999999999999", first.InputVersionID)
}

func TestFreezeImportInvalidRequestsNeitherCreateAnInputNorReserveItsIdentifiers(t *testing.T) {
	for _, test := range []struct {
		name       string
		invalidate func(*biz.InputImport)
	}{
		{"invalid tenant", func(c *biz.InputImport) { c.TenantID = "not-a-tenant" }},
		{"zero request", func(c *biz.InputImport) { c.RequestID = "00000000-0000-0000-0000-000000000000" }},
		{"malformed input separators", func(c *biz.InputImport) { c.InputVersionID = "33333333_3333_4333_8333_333333333333" }},
		{"missing actor", func(c *biz.InputImport) { c.Actor = "" }},
		{"actor control character", func(c *biz.InputImport) { c.Actor = "governance:user:42\n" }},
		{"missing audit time", func(c *biz.InputImport) { c.RequestedAt = time.Time{} }},
		{"unrepresentable audit precision", func(c *biz.InputImport) { c.RequestedAt = c.RequestedAt.Add(time.Nanosecond) }},
		{"missing connection", func(c *biz.InputImport) { c.Scope.StorageConnectionID = ""; c.Object.StorageConnectionID = "" }},
		{"connection outside owner scope", func(c *biz.InputImport) { c.Object.StorageConnectionID = "unapproved-connection" }},
		{"bucket outside owner scope", func(c *biz.InputImport) { c.Object.Bucket = "unapproved-bucket" }},
		{"missing bucket", func(c *biz.InputImport) { c.Scope.Bucket = ""; c.Object.Bucket = "" }},
		{"missing approved prefix", func(c *biz.InputImport) { c.Scope.ApprovedPrefix = "" }},
		{"prefix traversal", func(c *biz.InputImport) { c.Scope.ApprovedPrefix = "tenant/../input" }},
		{"parent prefix", func(c *biz.InputImport) { c.Scope.ApprovedPrefix = ".."; c.Object.Key = "../data.csv" }},
		{"leading parent prefix", func(c *biz.InputImport) { c.Scope.ApprovedPrefix = "../input"; c.Object.Key = "../input/data.csv" }},
		{"key traversal", func(c *biz.InputImport) { c.Object.Key = "tenant/input/../data.csv" }},
		{"prefix lookalike", func(c *biz.InputImport) { c.Object.Key = "tenant/input-other/data.csv" }},
		{"credential reference control character", func(c *biz.InputImport) { c.Scope.CredentialReference = "managed-secret:reader\n" }},
		{"missing object version", func(c *biz.InputImport) { c.Object.VersionID = nil }},
		{"unversioned null object", func(c *biz.InputImport) { version := "null"; c.Object.VersionID = &version }},
		{"unsupported immutable copy", func(c *biz.InputImport) { copy := true; c.Object.ImmutableCopy = &copy }},
		{"zero size", func(c *biz.InputImport) { c.Object.SizeBytes = 0 }},
		{"negative size", func(c *biz.InputImport) { c.Object.SizeBytes = -1 }},
		{"missing digest", func(c *biz.InputImport) { c.Object.SHA256 = "" }},
		{"noncanonical digest", func(c *biz.InputImport) { c.Object.SHA256 = strings.Repeat("A", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			repository := input.New(openPool())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			valid := importFixture()
			invalid := importFixture()
			test.invalidate(&invalid)
			got, err := repository.FreezeImport(ctx, invalid)
			if !errors.Is(err, biz.ErrInvalidInput) || !reflect.DeepEqual(got, biz.InputVersion{}) {
				t.Fatalf("invalid request produced a receipt: %+v, %v", got, err)
			}
			requireInputAbsent(t, ctx, repository, valid.TenantID, valid.InputVersionID)
			if got, err := repository.FreezeImport(ctx, valid); err != nil || !reflect.DeepEqual(got, biz.InputVersion{Import: valid, State: biz.InputStateValidating}) {
				t.Fatalf("rejected request reserved identifiers or changed the later valid import: %+v, %v", got, err)
			}
			requireFrozenImport(t, ctx, input.New(openPool()), valid)
		})
	}
}

func TestConcurrentFreezeImportExactRequestsAcrossIndependentPoolsReturnOneOriginalVersion(t *testing.T) {
	openPool := postgres.Prepare(t)
	repositories := [2]*input.Repository{input.New(openPool()), input.New(openPool())}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := importFixture()
	want := biz.InputVersion{Import: command, State: biz.InputStateValidating}
	for _, result := range freezeConcurrentImports(ctx, repositories, [2]biz.InputImport{command, command}) {
		if result.err != nil || !reflect.DeepEqual(result.version, want) {
			t.Fatalf("concurrent exact request did not replay the same durable version: %+v, %v", result.version, result.err)
		}
	}
	requireFrozenImport(t, ctx, input.New(openPool()), command)
}

func TestConcurrentFreezeImportDifferentFactsAcrossIndependentPoolsKeepOneStableWinner(t *testing.T) {
	openPool := postgres.Prepare(t)
	repositories := [2]*input.Repository{input.New(openPool()), input.New(openPool())}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, second := importFixture(), importFixture()
	second.Object.SHA256 = strings.Repeat("b", 64)
	results := freezeConcurrentImports(ctx, repositories, [2]biz.InputImport{first, second})
	succeeded, conflicted := 0, 0
	var winner, loser biz.InputImport
	for _, result := range results {
		if errors.Is(result.err, biz.ErrInputConflict) {
			if !reflect.DeepEqual(result.version, biz.InputVersion{}) {
				t.Fatal("conflicting request returned a usable receipt")
			}
			conflicted++
			loser = result.command
			continue
		}
		if result.err != nil || !reflect.DeepEqual(result.version, biz.InputVersion{Import: result.command, State: biz.InputStateValidating}) {
			t.Fatalf("unexpected competing import result: %+v, %v", result.version, result.err)
		}
		succeeded++
		winner = result.command
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("wanted one immutable winner and one conflict, got %d/%d", succeeded, conflicted)
	}
	reader := input.New(openPool())
	for range 3 {
		if got, err := reader.FreezeImport(ctx, winner); err != nil || !reflect.DeepEqual(got, biz.InputVersion{Import: winner, State: biz.InputStateValidating}) {
			t.Fatalf("winner did not remain replayable: %+v, %v", got, err)
		}
		if got, err := reader.FreezeImport(ctx, loser); !errors.Is(err, biz.ErrInputConflict) || !reflect.DeepEqual(got, biz.InputVersion{}) {
			t.Fatalf("loser did not remain a conflict: %+v, %v", got, err)
		}
		requireFrozenImport(t, ctx, reader, winner)
	}
}

func requireFrozenImport(t *testing.T, ctx context.Context, repository *input.Repository, want biz.InputImport) {
	t.Helper()
	got, err := repository.Get(ctx, want.TenantID, want.InputVersionID)
	if err != nil || !reflect.DeepEqual(got, biz.InputVersion{Import: want, State: biz.InputStateValidating}) {
		t.Fatalf("durable frozen import changed: %+v, %v", got, err)
	}
}

func requireInputAbsent(t *testing.T, ctx context.Context, repository *input.Repository, tenantID, inputID string) {
	t.Helper()
	got, err := repository.Get(ctx, tenantID, inputID)
	if !errors.Is(err, biz.ErrInputNotFound) || !reflect.DeepEqual(got, biz.InputVersion{}) {
		t.Fatalf("input should be absent for this tenant: %+v, %v", got, err)
	}
}

type frozenImportAttempt struct {
	command biz.InputImport
	version biz.InputVersion
	err     error
}

func freezeConcurrentImports(ctx context.Context, repositories [2]*input.Repository, commands [2]biz.InputImport) [2]frozenImportAttempt {
	start := make(chan struct{})
	completed := make(chan frozenImportAttempt, 2)
	for i := range repositories {
		go func(index int) {
			<-start
			version, err := repositories[index].FreezeImport(ctx, commands[index])
			completed <- frozenImportAttempt{command: commands[index], version: version, err: err}
		}(i)
	}
	close(start)
	return [2]frozenImportAttempt{<-completed, <-completed}
}
