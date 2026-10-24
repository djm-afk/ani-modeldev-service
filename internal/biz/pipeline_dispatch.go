package biz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// ErrPipelineDispatchBlocked rejects a first reservation after close or the
// original deadline. An existing reservation remains readable without a permit.
var ErrPipelineDispatchBlocked = errors.New("PIPELINE_DISPATCH_BLOCKED")

// PipelineOwnerConfiguration is an explicitly supplied owner configuration
// revision. Its digest is a reference, not proof that the configuration exists
// or that the control workload may use its storage. It contains no credentials.
type PipelineOwnerConfiguration struct {
	Reference      string `json:"reference"`
	RevisionSHA256 string `json:"revision_sha256"`
	PipelineRoot   string `json:"pipeline_root"`
}

type PipelineDispatchRequest struct {
	Admission Admission
	Owner     PipelineOwnerConfiguration
}

// PipelineDispatchPlan is internal owner state, separate from the shared
// execution snapshot. Admission.SpecHash does not cover Owner.PipelineRoot.
// Repositories accept a request and derive this plan from its validated
// admission; a caller-supplied plan is never an admission replacement.
type PipelineDispatchPlan struct {
	SchemaVersion     string                            `json:"schema_version"`
	TenantID          string                            `json:"tenant_id"`
	ExecutionID       string                            `json:"execution_id"`
	OperationID       string                            `json:"operation_id"`
	SpecHash          string                            `json:"spec_hash"`
	Owner             PipelineOwnerConfiguration        `json:"owner"`
	Environment       cpup01.EnvironmentBindingSnapshot `json:"environment"`
	PipelineID        string                            `json:"pipeline_id"`
	PipelineVersionID string                            `json:"pipeline_version_id"`
	DisplayName       string                            `json:"display_name"`
	DeadlineAt        time.Time                         `json:"deadline_at"`
}

var pipelineOwnerReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,511}$`)

// Freeze derives service account, experiment, pipeline identities and the
// execution/spec parameters exclusively from the existing admission contract.
// The repository must additionally compare that entire admission with its
// committed original under the shared execution identity lock.
func (request PipelineDispatchRequest) Freeze() (PipelineDispatchPlan, error) {
	_, snapshotBytes, err := request.Admission.CanonicalPayloads()
	if err != nil {
		return PipelineDispatchPlan{}, err
	}
	owner := request.Owner
	if !pipelineOwnerReferencePattern.MatchString(owner.Reference) || strings.Contains(owner.Reference, "://") || !closeSpecHashPattern.MatchString(owner.RevisionSHA256) {
		return PipelineDispatchPlan{}, ErrInvalidAdmission
	}
	if !ValidPipelineRoot(owner.PipelineRoot) {
		return PipelineDispatchPlan{}, ErrInvalidAdmission
	}
	var snapshot cpup01.Snapshot
	if json.Unmarshal(snapshotBytes, &snapshot) != nil {
		return PipelineDispatchPlan{}, ErrInvalidAdmission
	}
	return PipelineDispatchPlan{
		SchemaVersion: "ani.modeldev.pipeline-dispatch-plan.v1",
		TenantID:      strings.ToLower(request.Admission.TenantID),
		ExecutionID:   strings.ToLower(request.Admission.ExecutionID),
		OperationID:   strings.ToLower(request.Admission.OperationID),
		SpecHash:      request.Admission.SpecHash,
		Owner:         owner, Environment: snapshot.Environment,
		PipelineID: snapshot.Implementation.PipelineID, PipelineVersionID: snapshot.Implementation.PipelineVersionID,
		DisplayName: "md-" + strings.ToLower(request.Admission.ExecutionID),
		DeadlineAt:  snapshot.DeadlineAt,
	}, nil
}

// ValidPipelineRoot is the shared shape rule for the frozen owner plan and its
// KFP create request. It establishes neither storage access nor ownership.
func ValidPipelineRoot(value string) bool {
	root, err := url.Parse(value)
	return err == nil && root.Scheme == "s3" && root.Host != "" && root.User == nil && root.Port() == "" && root.RawQuery == "" && !root.ForceQuery && root.Fragment == "" && root.RawPath == "" && root.Path != "" && path.Clean(root.Path) == strings.TrimSuffix(root.Path, "/")
}

// Canonical encodes the internal plan in field order with no HTML escapes or
// trailing newline. Only Freeze establishes its relationship to an admission.
func (plan PipelineDispatchPlan) Canonical() ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(plan); err != nil {
		return nil, ErrInvalidAdmission
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func (plan PipelineDispatchPlan) Digest() (string, error) {
	canonical, err := plan.Canonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

type PipelineDispatchState string

const (
	PipelineDispatchSubmitting PipelineDispatchState = "SUBMITTING"
	PipelineDispatchNotSent    PipelineDispatchState = "SUBMISSION_NOT_SENT"
	PipelineDispatchUncertain  PipelineDispatchState = "SUBMISSION_UNCERTAIN"
	PipelineDispatchConfirmed  PipelineDispatchState = "SUBMISSION_CONFIRMED"
)

// PipelineConfirmedRun retains a trusted internal creation-response observation.
// It is neither independent network/Pod identity proof nor an authoritative Run.
// RunID is interpreted within the dispatch's frozen environment, not globally.
type PipelineConfirmedRun struct {
	RunID           string
	FirstObservedAt time.Time
}

type PipelineDispatch struct {
	// OwnerRevision covers the execution aggregate, including observations
	// which retain the same submission state. It is never a sending permit.
	OwnerRevision uint64
	AttemptID     string
	Plan          PipelineDispatchPlan
	PlanHash      string
	State         PipelineDispatchState
	ReservedAt    time.Time
	// UncertainAt is the first durably accepted uncertainty observation. It is
	// nil while SUBMITTING; replay cannot refresh it or grant another send.
	UncertainAt *time.Time
	// NotSentAt retains the first local no-send observation for this attempt.
	// It may coexist with later uncertainty/confirmed facts, never authorizes
	// another send, and is not an execution cancellation or training failure.
	NotSentAt *time.Time
	// ConfirmedRuns retains every distinct observed Run for this attempt, sorted
	// by canonical RunID. Position grants no priority or training permission.
	ConfirmedRuns []PipelineConfirmedRun
}

// PipelineSendPermit is returned only by the transaction which creates and
// successfully commits the first reservation. Reading a persisted dispatch or
// replaying Reserve must never reconstruct this permission.
type PipelineSendPermit struct {
	TenantID    string
	ExecutionID string
	AttemptID   string
	PlanHash    string
}

type PipelineDispatchReservation struct {
	Dispatch   PipelineDispatch
	SendPermit *PipelineSendPermit
}

// PipelineConfirmationReceipt acknowledges a committed observation.
// ConflictingRuns reports more than one distinct retained Run for the attempt;
// these are saved facts, not an error rollback or permission to adopt a Run.
type PipelineConfirmationReceipt struct {
	Dispatch        PipelineDispatch
	ConflictingRuns bool
}

type PipelineDispatchRepository interface {
	Reserve(context.Context, PipelineDispatchRequest) (PipelineDispatchReservation, error)
	Get(context.Context, string, string) (PipelineDispatch, error)
	// SubmissionObservationTime samples the reservation database after an
	// outbound observation. This is a database recording time, not the KFP
	// creation time, and never grants a send. It must not precede reserved_at.
	SubmissionObservationTime(context.Context, PipelineSendPermit) (time.Time, error)
	// MarkSubmissionNotSent retains a local no-send observation without
	// discarding stronger facts or reconstructing the original send permit.
	MarkSubmissionNotSent(context.Context, PipelineSendPermit, time.Time) (PipelineDispatch, error)
	// MarkSubmissionUncertain records only the original attempt's outcome;
	// it is permitted after close and never grants permission to send again.
	// A late uncertain observation cannot downgrade a confirmed dispatch.
	MarkSubmissionUncertain(context.Context, PipelineSendPermit, time.Time) (PipelineDispatch, error)
	// RecordSubmissionConfirmed accepts only a CONFIRMED internal observation
	// matching the original attempt/plan. It retains late handles after close,
	// without establishing authority, reopening execution or granting a send.
	// The observation time must be nonzero, within UTC years 1..9999, exact to
	// microseconds and not before reservation; replay preserves the first time.
	RecordSubmissionConfirmed(context.Context, PipelineSendPermit, PipelineSubmissionObservation, time.Time) (PipelineConfirmationReceipt, error)
}
