package cpup01

import (
	"fmt"
	"math"
)

// These finite structure guards are the current public contract. Content
// policy comes exclusively from the explicit ContentLimits value.
const (
	MaxModelFiles                = 1024
	MaxOutputFiles               = 2048
	MaxArchiveEntries            = 4096
	MaxRelativePathBytes         = 1024
	MaxStorageKeyBytes           = 1024
	MaxManifestBytes             = 8 * 1024 * 1024
	MaxMaterialBytes             = 1024 * 1024
	MaxModelConfigBytes          = 8 * 1024 * 1024
	MaxArchiveMetadataBytes      = 16 * 1024 * 1024
	MaxRecordBytes               = 32 * 1024 * 1024
	MaxJSONDepth                 = 64
	MaxControlRPCBytes           = 64 * 1024 * 1024
	MaxPublicationCandidateBytes = 32 * 1024 * 1024
	MaxPublicationRefBytes       = 1024
)

type ContentKind string

const (
	ContentDataset   ContentKind = "dataset"
	ContentBaseModel ContentKind = "base_model"
	ContentOutput    ContentKind = "output"
)

// ContentLimits is supplied at new admission and frozen into each execution.
// Stored-contract decoding validates its own frozen value, never live config.
type ContentLimits struct {
	MaxContentBytes int64 `json:"max_content_bytes,string"`
}

type LimitViolation struct {
	Reason    string
	Kind      string
	LimitName string
	Actual    int64
	Limit     int64
}

func (e *LimitViolation) Error() string {
	return fmt.Sprintf("%s: %s actual=%d limit=%d", e.Reason, e.Kind, e.Actual, e.Limit)
}

func (limits ContentLimits) Validate() error {
	_, err := TarReadBound(limits.MaxContentBytes)
	return err
}

func (limits ContentLimits) Check(kind ContentKind, actual int64) error {
	if limits.Validate() != nil || actual <= 0 || (kind != ContentDataset && kind != ContentBaseModel && kind != ContentOutput) {
		return fmt.Errorf("%w: explicit positive content limit and content size required", ErrInvalidArgument)
	}
	if actual > limits.MaxContentBytes {
		return &LimitViolation{Reason: "CONTENT_LIMIT_EXCEEDED", Kind: string(kind), LimitName: "max_content_bytes", Actual: actual, Limit: limits.MaxContentBytes}
	}
	return nil
}

// CheckedAdd and CheckedMultiply account only for nonnegative byte/count
// values. An unrepresentable layout is a current request/configuration error.
func CheckedAdd(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, fmt.Errorf("%w: byte count overflow", ErrInvalidArgument)
	}
	return left + right, nil
}

func CheckedMultiply(left, right int64) (int64, error) {
	if left < 0 || right < 0 || (right != 0 && left > math.MaxInt64/right) {
		return 0, fmt.Errorf("%w: byte count overflow", ErrInvalidArgument)
	}
	return left * right, nil
}

// TarReadBound keeps ordinary file content separate from bounded POSIX/PAX
// headers, padding, path metadata and the end marker. It never evaluates L+1.
func TarReadBound(contentBytes int64) (int64, error) {
	if contentBytes <= 0 {
		return 0, fmt.Errorf("%w: max_content_bytes must be positive", ErrInvalidArgument)
	}
	overhead := int64(512*MaxArchiveEntries + 511*MaxModelFiles + MaxArchiveMetadataBytes + 1024)
	return CheckedAdd(contentBytes, overhead)
}
