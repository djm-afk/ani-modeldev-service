package biz

import (
	"context"
	"time"
)

// This file expresses the trained-artifact registration contract that Model
// exposes as prepare/complete/abort. It is deliberately transport-agnostic: the
// gRPC adapter lives in internal/data/modelclient and is only one implementation
// of this port. Defining the contract here lets the orchestration, its durable
// arbitration and its tests be written and reviewed before the provider publishes
// generated stubs.

// TrainedArtifactPrepare freezes everything the provider needs to open one
// registration session. The identity of the target (an existing container or a
// new one) and the runtime configuration (inherited from a source version or
// spelled out) are provider decisions carried through unchanged.
type TrainedArtifactPrepare struct {
	// IdempotencyKey names one session. Replaying it must return the same
	// session rather than create a second version; changing the content under
	// the same key is a conflict.
	IdempotencyKey string
	// Existing-container branch.
	TargetModelID string
	// New-container branch. All four are provider-owned metadata.
	ModelName    string
	DisplayName  string
	Description  string
	Capabilities []string
	// Runtime inheritance. When set, the provider reuses the source version's
	// runtime instead of requiring an explicit engine triple.
	SourceModelVersionID string
	// Output content.
	Version        string
	FileName       string
	Format         string
	SizeBytes      int64
	ChecksumSHA256 string
}

// TrainedArtifactTicket is the opened session plus the controlled-copy ticket.
// The upload target is the provider's own storage path, never a caller value.
type TrainedArtifactTicket struct {
	RegistrationID string
	Upload         UploadTicket
}

// TrainedArtifactReceipt is the provider's settled result. ModelVersionStatus
// distinguishes a ready version from a still-pending one, so a successful call
// is never read as READY on its own.
type TrainedArtifactReceipt struct {
	RegistrationID     string
	ModelID            string
	ModelVersionID     string
	ModelVersionStatus string
	StoragePath        string
	SizeBytes          int64
	ChecksumSHA256     string
}

// TrainedArtifactRegistration is the narrow outbound port for the registration
// session. Implementations own only transport, caller identity and metadata;
// they never decide whether a write is permitted, and they never retry on their
// own. Prepare and Complete are idempotent under the session's own key, which is
// what makes an unresolved attempt recoverable without a second session.
type TrainedArtifactRegistration interface {
	// PrepareTrainedArtifact opens or replays one registration session.
	PrepareTrainedArtifact(context.Context, string, TrainedArtifactPrepare) (TrainedArtifactTicket, error)
	// CompleteTrainedArtifact finalises the session after the object landed. A
	// repeat returns the same receipt and never creates a second version.
	CompleteTrainedArtifact(context.Context, string, string, string) (TrainedArtifactReceipt, error)
	// AbortTrainedArtifact terminates a session that will not be completed.
	AbortTrainedArtifact(context.Context, string, string, string) error
}

// TrainedArtifactReady is the only provider status that may become READY.
const TrainedArtifactReady = "ready"

// registrationSessionID is the bounded trace value stored for an opened session
// so an unresolved attempt can be re-completed without opening a new one.
func (ticket TrainedArtifactTicket) sessionID() string {
	return ticket.RegistrationID
}

// UploadTicketClock reports whether the provider ticket is still usable. A
// ticket that expired must be re-prepared rather than uploaded against.
func ticketUsable(ticket UploadTicket, now time.Time) bool {
	return ticket.URL != "" && !ticket.ExpiresAt.IsZero() && ticket.ExpiresAt.After(now)
}
