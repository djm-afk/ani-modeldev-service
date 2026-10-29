package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// IdentityRef fixes the trusted candidate before any external resolution.
type IdentityRef struct {
	TenantID    string `json:"tenant_id"`
	Actor       string `json:"actor"`
	ExecutionID string `json:"execution_id"`
	OperationID string `json:"operation_id"`
	IntentHash  string `json:"intent_hash"`
}

type ImplementationSnapshot struct {
	ImplementationID         string `json:"implementation_id"`
	Version                  string `json:"version"`
	ParameterContractVersion string `json:"parameter_contract_version"`
	PipelineID               string `json:"pipeline_id"`
	PipelineVersionID        string `json:"pipeline_version_id"`
	PipelineIRSHA256         string `json:"pipeline_ir_sha256"`
}

// Timing contains one immutable normal-business budget. Close and cleanup use
// their separately bounded owner configuration, never this expired deadline.
type Timing struct {
	AcceptedAt                 time.Time `json:"accepted_at"`
	ExecutionTimeoutSeconds    uint64    `json:"execution_timeout_seconds"`
	ExecutionTimeoutMaxSeconds uint64    `json:"execution_timeout_max_seconds"`
	StepTimeoutSeconds         uint64    `json:"step_timeout_seconds"`
	ExecutionDeadlineAt        time.Time `json:"execution_deadline_at"`
}

type TimingConfiguration struct {
	ExecutionTimeoutDefaultSeconds uint64 `json:"execution_timeout_default_seconds"`
	ExecutionTimeoutMaxSeconds     uint64 `json:"execution_timeout_max_seconds"`
	StepTimeoutSeconds             uint64 `json:"step_timeout_seconds"`
}

func (c TimingConfiguration) Resolve(accepted time.Time, explicit *uint64) (Timing, error) {
	maxSeconds := uint64(math.MaxInt64 / int64(time.Second))
	if c.ExecutionTimeoutDefaultSeconds == 0 || c.ExecutionTimeoutMaxSeconds == 0 || c.StepTimeoutSeconds == 0 || c.ExecutionTimeoutDefaultSeconds > c.ExecutionTimeoutMaxSeconds || c.StepTimeoutSeconds > c.ExecutionTimeoutMaxSeconds || c.ExecutionTimeoutMaxSeconds > maxSeconds {
		return Timing{}, fmt.Errorf("%w: timing configuration", ErrInvalidArgument)
	}
	seconds := c.ExecutionTimeoutDefaultSeconds
	if explicit != nil {
		seconds = *explicit
	}
	if seconds == 0 || seconds > c.ExecutionTimeoutMaxSeconds || accepted.IsZero() || accepted.Year() < 1 || accepted.Year() > 9999 || accepted.Nanosecond()%1000 != 0 {
		return Timing{}, fmt.Errorf("%w: timing", ErrInvalidArgument)
	}
	deadline := accepted.Add(time.Duration(seconds) * time.Second).UTC()
	if !deadline.After(accepted) || deadline.Year() > 9999 {
		return Timing{}, fmt.Errorf("%w: timing overflow", ErrInvalidArgument)
	}
	return Timing{AcceptedAt: accepted.UTC(), ExecutionTimeoutSeconds: seconds, ExecutionTimeoutMaxSeconds: c.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: c.StepTimeoutSeconds, ExecutionDeadlineAt: deadline}, nil
}

func (t Timing) Validate() error {
	v, err := (TimingConfiguration{ExecutionTimeoutDefaultSeconds: t.ExecutionTimeoutSeconds, ExecutionTimeoutMaxSeconds: t.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: t.StepTimeoutSeconds}).Resolve(t.AcceptedAt, nil)
	if err != nil || !v.ExecutionDeadlineAt.Equal(t.ExecutionDeadlineAt) {
		return fmt.Errorf("%w: frozen timing", ErrInvalidArgument)
	}
	return nil
}

// DeadlineFor is pure: its caller persists firstAdmitted under the execution
// row lock once. Retries read that fact and cannot extend this window.
func (t Timing) DeadlineFor(firstAdmitted time.Time) (time.Time, error) {
	if t.Validate() != nil || firstAdmitted.IsZero() || firstAdmitted.Before(t.AcceptedAt) || firstAdmitted.Nanosecond()%1000 != 0 {
		return time.Time{}, ErrInvalidArgument
	}
	deadline := firstAdmitted.Add(time.Duration(t.StepTimeoutSeconds) * time.Second).UTC()
	if t.ExecutionDeadlineAt.Before(deadline) {
		deadline = t.ExecutionDeadlineAt
	}
	return deadline, nil
}

// ComputeOrigin retains the official provider's canonical selection bytes.
// CPU/memory here are the provider's single-unit values, not an independent
// catalogue. Provider adapters must validate SelectionJSON with its helper.
type ComputeOrigin struct {
	ComputePresetID string `json:"compute_preset_id"`
	SelectionDigest string `json:"selection_digest"`
	SelectionJSON   string `json:"selection"`
	CPUMillis       int64  `json:"cpu_millis,string"`
	MemoryMiB       int64  `json:"memory_mib,string"`
	DeviceMode      string `json:"device_mode"`
}

// GPURequest fixes the exact current single-container request handed to ACC.
// Mode and limits are original selection evidence for cross-provider checks;
// they are not caller-chosen GPU resources or another quota ledger.
type GPURequest struct {
	ClusterID          string `json:"cluster_id"`
	PoolID             string `json:"pool_id"`
	ProfileID          string `json:"profile_id"`
	ProfileVersion     uint64 `json:"profile_version,string"`
	Replicas           uint32 `json:"replicas"`
	DevicesPerReplica  uint32 `json:"devices_per_replica"`
	ContainerName      string `json:"container_name"`
	SpecDigest         string `json:"spec_digest"`
	Mode               string `json:"mode"`
	ModelKey           string `json:"model_key"`
	MemoryMiBPerDevice int64  `json:"memory_mib_per_device,string"`
	CoreLimitPercent   uint32 `json:"core_limit_percent"`
}

func GPURequestFor(origin ComputeOrigin) (*GPURequest, error) {
	if origin.Validate() != nil {
		return nil, ErrInvalidArgument
	}
	if origin.DeviceMode == "cpu" {
		return nil, nil
	}
	var source struct {
		GPU struct {
			ClusterID          string `json:"cluster_id"`
			PoolID             string `json:"pool_id"`
			ProfileID          string `json:"profile_id"`
			ProfileVersion     uint64 `json:"profile_version"`
			DevicesPerUnit     uint32 `json:"devices_per_unit"`
			SpecDigest         string `json:"spec_digest"`
			Mode               string `json:"mode"`
			ModelKey           string `json:"model_key"`
			MemoryMiBPerDevice int64  `json:"memory_mib_per_device"`
			CoreLimitPercent   uint32 `json:"core_limit_percent"`
		} `json:"gpu"`
	}
	if json.Unmarshal([]byte(origin.SelectionJSON), &source) != nil {
		return nil, ErrInvalidArgument
	}
	g := source.GPU
	if !validUUID(g.ClusterID) || !validUUID(g.PoolID) || !validUUID(g.ProfileID) || g.ProfileVersion == 0 || g.DevicesPerUnit != 1 || !snapshotSHA256Pattern.MatchString(g.SpecDigest) || g.MemoryMiBPerDevice <= 0 || !validSnapshotReference(g.ModelKey) || (g.Mode != "WHOLE_EXCLUSIVE" && g.Mode != "SHARED_FIXED") || g.CoreLimitPercent == 0 || g.CoreLimitPercent > 100 || g.Mode == "WHOLE_EXCLUSIVE" && g.CoreLimitPercent != 100 || g.Mode == "SHARED_FIXED" && g.CoreLimitPercent >= 100 {
		return nil, ErrInvalidArgument
	}
	return &GPURequest{ClusterID: g.ClusterID, PoolID: g.PoolID, ProfileID: g.ProfileID, ProfileVersion: g.ProfileVersion, Replicas: 1, DevicesPerReplica: 1, ContainerName: "node", SpecDigest: g.SpecDigest, Mode: g.Mode, ModelKey: g.ModelKey, MemoryMiBPerDevice: g.MemoryMiBPerDevice, CoreLimitPercent: g.CoreLimitPercent}, nil
}

func (c ComputeOrigin) Validate() error {
	if !validUUID(c.ComputePresetID) || !snapshotSHA256Pattern.MatchString(c.SelectionDigest) || c.CPUMillis <= 0 || c.MemoryMiB <= 0 || c.MemoryMiB > math.MaxInt64/(1024*1024) || (c.DeviceMode != "cpu" && c.DeviceMode != "gpu") || len(c.SelectionJSON) == 0 || len(c.SelectionJSON) > 16384 || !json.Valid([]byte(c.SelectionJSON)) {
		return ErrInvalidArgument
	}
	digest := sha256.Sum256(append([]byte("compute-selection-v1\n"), []byte(c.SelectionJSON)...))
	if hex.EncodeToString(digest[:]) != c.SelectionDigest {
		return ErrInvalidArgument
	}
	var fields struct {
		PresetID    string `json:"preset_id"`
		CPUMillis   int64  `json:"cpu_millis"`
		MemoryMiB   int64  `json:"memory_mib"`
		ComputeType string `json:"compute_type"`
	}
	if json.Unmarshal([]byte(c.SelectionJSON), &fields) != nil || fields.PresetID != c.ComputePresetID || fields.CPUMillis != c.CPUMillis || fields.MemoryMiB != c.MemoryMiB || fields.ComputeType != c.DeviceMode {
		return ErrInvalidArgument
	}
	return nil
}

// RuntimePlan contains only desired pre-admission bytes. Live UIDs/status and
// execution_spec_hash are deliberately absent to prevent hash self-reference.
type RuntimePlan struct {
	GeneratorVersion   string          `json:"generator_version"`
	APIGroup           string          `json:"api_group"`
	Kind               string          `json:"kind"`
	Namespace          string          `json:"namespace"`
	Name               string          `json:"name"`
	ExpectedSpec       json.RawMessage `json:"expected_spec"`
	ExpectedSpecSHA256 string          `json:"expected_spec_sha256"`
	DefaultingRules    string          `json:"defaulting_rules"`
	TargetJobs         []string        `json:"target_jobs"`
}

func (p RuntimePlan) Reference() RuntimeRef {
	return RuntimeRef{Name: p.Name, Kind: p.Kind, APIGroup: p.APIGroup, ContentSHA256: p.ExpectedSpecSHA256, TargetJobs: append([]string{}, p.TargetJobs...)}
}

func RuntimeName(namespaceUID, executionID string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(namespaceUID) + "\n" + strings.ToLower(executionID)))
	return "mdrt-" + hex.EncodeToString(digest[:])[:48]
}

func (p RuntimePlan) Validate() error {
	if p.GeneratorVersion == "" || p.Kind != "TrainingRuntime" || p.APIGroup != "trainer.kubeflow.org" || !validSnapshotDNSLabel(p.Namespace) || !validSnapshotDNSLabel(p.Name) || p.DefaultingRules == "" || len(p.ExpectedSpec) == 0 || len(p.ExpectedSpec) > 256*1024 || !json.Valid(p.ExpectedSpec) || len(p.TargetJobs) != 1 || p.TargetJobs[0] != "node" {
		return fmt.Errorf("%w: runtime plan", ErrInvalidArgument)
	}
	var decoded any
	if json.Unmarshal(p.ExpectedSpec, &decoded) != nil {
		return ErrInvalidArgument
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: runtime policy", ErrInvalidArgument)
	}
	policy, ok := object["podGroupPolicy"].(map[string]any)
	if !ok || len(policy) != 1 {
		return fmt.Errorf("%w: runtime policy", ErrInvalidArgument)
	}
	volcano, ok := policy["volcano"].(map[string]any)
	if !ok || len(volcano) != 0 {
		return fmt.Errorf("%w: runtime policy", ErrInvalidArgument)
	}
	canonical, err := encodeCanonicalJSON(decoded)
	if err != nil || string(canonical) != string(p.ExpectedSpec) {
		return fmt.Errorf("%w: runtime canonical spec", ErrInvalidArgument)
	}
	digest := sha256.Sum256(p.ExpectedSpec)
	if hex.EncodeToString(digest[:]) != p.ExpectedSpecSHA256 {
		return fmt.Errorf("%w: runtime spec hash", ErrInvalidArgument)
	}
	return nil
}
