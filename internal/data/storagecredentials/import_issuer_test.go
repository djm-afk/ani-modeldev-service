package storagecredentials

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

type fakeLedger struct {
	claimErr   error
	generation int64
	issued     []time.Time
	notIssued  int
	unknown    int
	bound      *time.Time
}

func (f *fakeLedger) ClaimGeneration(ctx context.Context, request ImportIssuanceRequest) (int64, error) {
	return f.generation, f.claimErr
}
func (f *fakeLedger) RecordIssued(ctx context.Context, issuanceID string, at time.Time) error {
	f.issued = append(f.issued, at)
	return nil
}
func (f *fakeLedger) RecordNotIssued(ctx context.Context, issuanceID string) error {
	f.notIssued++
	return nil
}
func (f *fakeLedger) RecordUnknown(ctx context.Context, issuanceID string, bound *time.Time, basis []byte) error {
	f.unknown++
	f.bound = bound
	return nil
}

type fakeSTS struct {
	issued      bool
	credentials biz.TemporaryStorageCredentials
	err         error
}

func (f *fakeSTS) Issue(ctx context.Context, grant ImportGrant, deadline time.Time) (bool, biz.TemporaryStorageCredentials, error) {
	return f.issued, f.credentials, f.err
}

const (
	issuerTenant    = "11111111-1111-4111-8111-111111111111"
	issuerDataKey   = "inputs/11111111-1111-4111-8111-111111111111/data/44444444-4444-4444-8444-444444444444/data.json"
	issuerResultKey = "inputs/11111111-1111-4111-8111-111111111111/result/44444444-4444-4444-8444-444444444444/result.json"
	issuerPolicySHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func baseRequest(now time.Time) ImportIssuanceRequest {
	return ImportIssuanceRequest{
		TenantID: issuerTenant, DatasetID: "22222222-2222-4222-8222-222222222222",
		TaskID: "33333333-3333-4333-8333-333333333333", AttemptID: "44444444-4444-4444-8444-444444444444",
		IssuanceID: "55555555-5555-4555-8555-555555555555", SecretUID: "66666666-6666-4666-8666-666666666666",
		TaskLeaseEpoch: 3, ReconcileEpoch: 2,
		Policy: []byte(`{"Statement":[]}`), PolicySHA256: issuerPolicySHA,
		IntendedAt: now, RequestDeadline: now.Add(time.Minute), AuthDeadline: now.Add(10 * time.Minute),
	}
}

func baseGrant() ImportGrant {
	return ImportGrant{
		StorageConnectionID: "input-store-v1", Bucket: "inputs",
		DataKey: issuerDataKey, ResultKey: issuerResultKey,
	}
}

func newIssuer(t *testing.T, ledger ImportIssuanceLedger, sts ImportSTS, now time.Time) *DatasetImportIssuer {
	t.Helper()
	issuer, err := NewDatasetImportIssuer(ledger, sts)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	issuer.now = func() time.Time { return now }
	return issuer
}

func TestIssueForImportRecordsIssuedExpiry(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{generation: 1}
	// Inside the 10-minute attempt authorization window: a session that outlived
	// it would outlive the attempt that authorized it.
	expiry := now.Add(8 * time.Minute)
	sts := &fakeSTS{issued: true, credentials: biz.TemporaryStorageCredentials{
		AccessKeyID: "a", SecretAccessKey: "b", SessionToken: "c", ExpiresAt: expiry,
	}}
	request := baseRequest(now)
	// The session must cover the attempt window, so the deadline sits inside it.
	request.AuthDeadline = now.Add(5 * time.Minute)
	outcome, err := newIssuer(t, ledger, sts, now).IssueForImport(context.Background(), request, baseGrant())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if outcome.State != "ISSUED" || !outcome.ExpiresAt.Equal(expiry) || len(ledger.issued) != 1 {
		t.Fatalf("outcome = %+v ledger.issued=%v", outcome, ledger.issued)
	}
}

// A session that outlives the attempt window is accepted: the policy is already
// scoped to this attempt's two keys and the attempt stops at its deadline, so a
// longer session grants nothing extra. Refusing it would only reject a backend
// that rounds its expiry upward.
func TestIssueForImportAcceptsSessionBeyondAuthorizationWindow(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{generation: 1}
	sts := &fakeSTS{issued: true, credentials: biz.TemporaryStorageCredentials{
		AccessKeyID: "a", SecretAccessKey: "b", SessionToken: "c", ExpiresAt: now.Add(15 * time.Minute),
	}}
	outcome, err := newIssuer(t, ledger, sts, now).IssueForImport(context.Background(), baseRequest(now), baseGrant())
	if err != nil {
		t.Fatalf("a longer session was refused: %v", err)
	}
	if outcome.State != "ISSUED" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// A session shorter than the attempt window cannot carry the upload that follows
// a slow download. The Secret is written once and never refreshed, so a short
// session fails the import halfway through with an opaque AccessDenied.
func TestIssueForImportRejectsSessionShorterThanTheAttemptWindow(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	request := baseRequest(now)
	// The attempt still needs ten minutes; the backend returns five.
	request.AuthDeadline = now.Add(10 * time.Minute)
	ledger := &fakeLedger{generation: 1}
	sts := &fakeSTS{issued: true, credentials: biz.TemporaryStorageCredentials{
		AccessKeyID: "a", SecretAccessKey: "b", SessionToken: "c", ExpiresAt: now.Add(5 * time.Minute),
	}}
	if _, err := newIssuer(t, ledger, sts, now).IssueForImport(context.Background(), request, baseGrant()); !errors.Is(err, ErrImportIssuanceUnavailable) {
		t.Fatalf("a session shorter than the attempt window was accepted: %v", err)
	}
}

// A session that covers the window exactly is usable: the rule is "at least the
// deadline", not "strictly more".
func TestIssueForImportAcceptsASessionThatCoversTheWindow(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	request := baseRequest(now)
	request.AuthDeadline = now.Add(10 * time.Minute)
	ledger := &fakeLedger{generation: 1}
	sts := &fakeSTS{issued: true, credentials: biz.TemporaryStorageCredentials{
		AccessKeyID: "a", SecretAccessKey: "b", SessionToken: "c", ExpiresAt: now.Add(10 * time.Minute),
	}}
	outcome, err := newIssuer(t, ledger, sts, now).IssueForImport(context.Background(), request, baseGrant())
	if err != nil {
		t.Fatalf("a covering session was refused: %v", err)
	}
	if outcome.State != "ISSUED" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestIssueForImportRecordsNotIssuedOnlyWhenProven(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{generation: 2}
	outcome, err := newIssuer(t, ledger, &fakeSTS{issued: false}, now).IssueForImport(context.Background(), baseRequest(now), baseGrant())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if outcome.State != "NOT_ISSUED" || ledger.notIssued != 1 {
		t.Fatalf("outcome = %+v notIssued=%d", outcome, ledger.notIssued)
	}
}

func TestIssueForImportTreatsErrorsAsUnknownNotNotIssued(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{generation: 3}
	outcome, err := newIssuer(t, ledger, &fakeSTS{err: errors.New("timeout")}, now).IssueForImport(context.Background(), baseRequest(now), baseGrant())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if outcome.State != "UNKNOWN" || ledger.unknown != 1 || ledger.notIssued != 0 {
		t.Fatalf("outcome = %+v unknown=%d notIssued=%d", outcome, ledger.unknown, ledger.notIssued)
	}
	// No evidence for a bound, so GC must keep blocking.
	if ledger.bound != nil {
		t.Fatal("unknown outcome invented an upper bound")
	}
}

func TestIssueForImportRejectsExpiredOrInvertedDeadlines(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*ImportIssuanceRequest){
		"auth already passed":   func(r *ImportIssuanceRequest) { r.AuthDeadline = now.Add(-time.Minute) },
		"request after auth":    func(r *ImportIssuanceRequest) { r.RequestDeadline = r.AuthDeadline.Add(time.Minute) },
		"request deadline past": func(r *ImportIssuanceRequest) { r.RequestDeadline = now.Add(-time.Minute) },
	} {
		request := baseRequest(now)
		mutate(&request)
		_, err := newIssuer(t, &fakeLedger{generation: 1}, &fakeSTS{issued: true}, now).IssueForImport(context.Background(), request, baseGrant())
		if !errors.Is(err, ErrImportIssuanceUnavailable) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestIssueForImportRejectsWeakGrant(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for name, grant := range map[string]ImportGrant{
		"same keys":    {StorageConnectionID: "s", Bucket: "b", DataKey: "a/b.json", ResultKey: "a/b.json"},
		"wildcard":     {StorageConnectionID: "s", Bucket: "b", DataKey: "a/*.json", ResultKey: "a/c.json"},
		"empty bucket": {StorageConnectionID: "s", DataKey: "a/b.json", ResultKey: "a/c.json"},
	} {
		_, err := newIssuer(t, &fakeLedger{generation: 1}, &fakeSTS{issued: true}, now).IssueForImport(context.Background(), baseRequest(now), grant)
		if !errors.Is(err, ErrImportIssuanceUnavailable) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestIssueForImportPropagatesFenceRejection(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{claimErr: ErrImportIssuanceFence}
	_, err := newIssuer(t, ledger, &fakeSTS{issued: true}, now).IssueForImport(context.Background(), baseRequest(now), baseGrant())
	if !errors.Is(err, ErrImportIssuanceFence) {
		t.Fatalf("fence rejection returned %v", err)
	}
}

func TestGCVisibleUntilBlocksOnUnboundedUnknown(t *testing.T) {
	expiry := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	bound := expiry.Add(time.Hour)
	// A single UNKNOWN with no bound must block everything, even with later
	// ISSUED rows present: MAX() would have silently ignored the NULL.
	if _, ok := GCVisibleUntil([]ImportIssuanceFact{
		{Outcome: "ISSUED", ActualExpiresAt: expiry},
		{Outcome: "UNKNOWN", UpperBound: nil},
	}); ok {
		t.Fatal("unbounded unknown did not block collection")
	}
	// With a proven bound, the latest of every relevant fact wins.
	until, ok := GCVisibleUntil([]ImportIssuanceFact{
		{Outcome: "ISSUED", ActualExpiresAt: expiry},
		{Outcome: "UNKNOWN", UpperBound: &bound},
		{Outcome: "NOT_ISSUED"},
	})
	if !ok || !until.Equal(bound) {
		t.Fatalf("until=%v ok=%v", until, ok)
	}
	// An earlier bound must not shrink a later real expiry.
	later := expiry.Add(2 * time.Hour)
	until, ok = GCVisibleUntil([]ImportIssuanceFact{
		{Outcome: "ISSUED", ActualExpiresAt: later},
		{Outcome: "INTENT", UpperBound: &bound},
	})
	if !ok || !until.Equal(later) {
		t.Fatalf("conservative expiry lost: %v", until)
	}
}

// STS rejects a RoleSessionName longer than 64 characters. Concatenating two
// UUIDs produced an 89-character name, so every import issuance failed before
// any request left the process.
func TestImportSessionNameFitsTheRoleSessionLimit(t *testing.T) {
	name := importSessionName(biz.ImportSessionRequest{
		TaskID: "482b31c0-e503-4d95-b8a1-2623f3c161ac", AttemptID: "eb281562-7d3e-4eb0-bd33-1699bb5b5d2e",
	})
	if len(name) > 64 {
		t.Fatalf("session name is %d characters: %q", len(name), name)
	}
	if name == "" {
		t.Fatal("session name is empty")
	}
	// Different attempts must not collapse onto the same session name.
	other := importSessionName(biz.ImportSessionRequest{
		TaskID: "482b31c0-e503-4d95-b8a1-2623f3c161ac", AttemptID: "cd36a43b-96fd-48e1-ae2f-fd9a23911d51",
	})
	if name == other {
		t.Fatalf("distinct attempts share a session name: %q", name)
	}
}
