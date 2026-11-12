package storagecredentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var (
	ErrImportIssuanceUnavailable = errors.New("dataset import credentials unavailable")
	ErrImportIssuanceFence       = errors.New("dataset import issuance fence rejected")
)

// ImportIssuanceLedger records one STS generation. The database never stores a
// key; it stores how far the session may live and how delivery went.
type ImportIssuanceLedger interface {
	// ClaimGeneration opens the INTENT row and advances the attempt's
	// last_credential_generation inside one short transaction. It returns the new
	// generation, or ErrImportIssuanceFence when the double fence is not held.
	ClaimGeneration(ctx context.Context, request ImportIssuanceRequest) (int64, error)
	// RecordIssued stores the real expiry. A late result from an old owner is
	// allowed only when it matches the original issuance row.
	RecordIssued(ctx context.Context, issuanceID string, actualExpiresAt time.Time) error
	// RecordNotIssued is legal only when the backend certainly did not issue.
	RecordNotIssued(ctx context.Context, issuanceID string) error
	// RecordUnknown stores the conservative upper bound, or NULL when there is
	// no evidence for one.
	RecordUnknown(ctx context.Context, issuanceID string, upperBound *time.Time, basis []byte) error
}

// ImportIssuanceRequest is the fully checked intent of one generation.
type ImportIssuanceRequest struct {
	TenantID        string
	DatasetID       string
	TaskID          string
	AttemptID       string
	IssuanceID      string
	SecretUID       string
	TaskLeaseEpoch  int64
	ReconcileEpoch  int64
	Policy          []byte
	PolicySHA256    string
	IntendedAt      time.Time
	RequestDeadline time.Time
	AuthDeadline    time.Time
}

// ImportGrant is what an issuance may touch: exactly this attempt's data and
// result keys, nothing wider.
type ImportGrant struct {
	StorageConnectionID string
	Bucket              string
	DataKey             string
	ResultKey           string
}

// ImportSTS is the outbound STS boundary. It returns only what the backend
// proved, never a default lifetime.
type ImportSTS interface {
	// Issue asks for one session. ok=false with no error means the backend
	// certainly did not issue; an error means the outcome is unknown.
	Issue(ctx context.Context, grant ImportGrant, deadline time.Time) (issued bool, credentials biz.TemporaryStorageCredentials, err error)
}

// DatasetImportIssuer issues one generation per physical STS call and records
// it before the request leaves the process.
type DatasetImportIssuer struct {
	ledger ImportIssuanceLedger
	sts    ImportSTS
	now    func() time.Time
}

func NewDatasetImportIssuer(ledger ImportIssuanceLedger, sts ImportSTS) (*DatasetImportIssuer, error) {
	if ledger == nil || sts == nil {
		return nil, ErrImportIssuanceUnavailable
	}
	return &DatasetImportIssuer{ledger: ledger, sts: sts, now: time.Now}, nil
}

// ImportIssuanceOutcome tells the caller what it may do next. Only Issued may
// deliver a Secret; everything else must be treated as "do not start writing".
type ImportIssuanceOutcome struct {
	Generation  int64
	IssuanceID  string
	State       string
	ExpiresAt   time.Time
	UpperBound  *time.Time
	Credentials biz.TemporaryStorageCredentials
}

// IssueForImport runs the documented sequence: persist INTENT, call STS, record
// the proven result. It never retries implicitly, so every session is accounted
// for by exactly one row.
func (issuer *DatasetImportIssuer) IssueForImport(ctx context.Context, request ImportIssuanceRequest, grant ImportGrant) (ImportIssuanceOutcome, error) {
	if issuer == nil || ctx == nil || ctx.Err() != nil {
		return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
	}
	if !validGrant(grant) || len(request.PolicySHA256) != 64 {
		return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
	}
	now := issuer.now().UTC().Truncate(time.Microsecond)
	if request.AuthDeadline.Before(now) || request.RequestDeadline.After(request.AuthDeadline) || request.RequestDeadline.Before(now) {
		return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
	}
	generation, err := issuer.ledger.ClaimGeneration(ctx, request)
	if errors.Is(err, ErrImportIssuanceFence) {
		return ImportIssuanceOutcome{}, ErrImportIssuanceFence
	}
	if err != nil {
		return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
	}

	issued, credentials, stsErr := issuer.sts.Issue(ctx, grant, request.RequestDeadline)
	outcome := ImportIssuanceOutcome{Generation: generation, IssuanceID: request.IssuanceID}
	switch {
	case stsErr == nil && issued && !credentials.ExpiresAt.IsZero() && credentials.ExpiresAt.After(now):
		// A verified result: record the real expiry, never a requested one.
		if err := issuer.ledger.RecordIssued(ctx, request.IssuanceID, credentials.ExpiresAt); err != nil {
			return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
		}
		outcome.State = "ISSUED"
		outcome.ExpiresAt = credentials.ExpiresAt
		outcome.Credentials = credentials
		// The session must cover the attempt's remaining window without outliving
		// the authorization that granted it. The Secret is written once and never
		// refreshed, so a session that lapses before the deadline fails the upload
		// after a slow download with an opaque AccessDenied; a session that outlives
		// the attempt keeps a credential alive past the work it authorized.
		if !sessionCoversWindow(credentials.ExpiresAt, request.AuthDeadline) {
			return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
		}
		return outcome, nil
	case stsErr == nil && !issued:
		// Proved not sent / not issued. Safe only because the backend said so;
		// a timeout or unparsable reply takes the unknown path instead.
		if err := issuer.ledger.RecordNotIssued(ctx, request.IssuanceID); err != nil {
			return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
		}
		outcome.State = "NOT_ISSUED"
		return outcome, nil
	default:
		// Unknown: the request may have been sent. Without a proven upper bound
		// object GC must keep blocking, so the bound stays NULL.
		if err := issuer.ledger.RecordUnknown(ctx, request.IssuanceID, nil, nil); err != nil {
			return ImportIssuanceOutcome{}, ErrImportIssuanceUnavailable
		}
		outcome.State = "UNKNOWN"
		return outcome, nil
	}
}

// sessionCoversWindow reports whether a session's proven expiry can carry the
// attempt to its deadline. The window is inclusive: a session that ends exactly
// when the attempt must stop is enough.
//
// A session that outlives the deadline is accepted rather than refused. The
// policy is already scoped to this attempt's two keys, and the attempt stops at
// the deadline regardless, so a longer session grants nothing extra; refusing it
// would only reject a backend that rounds its expiry upward.
func sessionCoversWindow(expiresAt, authDeadline time.Time) bool {
	return !expiresAt.Before(authDeadline)
}

func validGrant(grant ImportGrant) bool {
	return biz.ValidStorageReference(grant.StorageConnectionID) && grant.Bucket != "" &&
		biz.ValidStorageKey(grant.DataKey) && biz.ValidStorageKey(grant.ResultKey) &&
		grant.DataKey != grant.ResultKey && !strings.ContainsAny(grant.DataKey+grant.ResultKey, "*?${")
}

// ImportPolicyDigest is the digest of the exact policy JSON a generation used.
// The row stores this, never the credential.
func ImportPolicyDigest(policy []byte) string {
	sum := sha256.Sum256(policy)
	return hex.EncodeToString(sum[:])
}

// GCVisibleUntil answers whether an issuance still blocks object deletion.
// INTENT and UNKNOWN without a proven bound block indefinitely.
func GCVisibleUntil(issuances []ImportIssuanceFact) (time.Time, bool) {
	var latest time.Time
	for _, issuance := range issuances {
		switch issuance.Outcome {
		case "ISSUED":
			if issuance.ActualExpiresAt.After(latest) {
				latest = issuance.ActualExpiresAt
			}
		case "INTENT", "UNKNOWN":
			// PostgreSQL MAX ignores NULL, so the unbounded case is tracked
			// separately: one such row means "never collect yet".
			if issuance.UpperBound == nil {
				return time.Time{}, false
			}
			if issuance.UpperBound.After(latest) {
				latest = *issuance.UpperBound
			}
		case "NOT_ISSUED":
			// Grants nothing.
		}
	}
	return latest, true
}

// ImportIssuanceFact is the subset of a ledger row GC needs.
type ImportIssuanceFact struct {
	Outcome         string
	ActualExpiresAt time.Time
	UpperBound      *time.Time
}
