package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
)

const CandidateSchemaVersion = "ani.modeldev.prepared-candidate.v3"

// PreparedCandidate is a complete frozen read-only observation. It is neither
// a durable receipt nor authority to start compute. Finalize only adds the
// final runtime plan and applicable accelerator result to this same snapshot.
type PreparedCandidate struct {
	SchemaVersion string      `json:"schema_version"`
	Intent        Intent      `json:"intent"`
	Snapshot      Snapshot    `json:"snapshot"`
	GPURequest    *GPURequest `json:"gpu_request,omitempty"`
}

func (c PreparedCandidate) Validate() error {
	if c.SchemaVersion != CandidateSchemaVersion {
		return ErrInvalidArgument
	}
	_, hash, err := CanonicalIntent(c.Intent)
	if err != nil || hash != c.Snapshot.IdentityRef.IntentHash {
		return ErrInvalidArgument
	}
	s := c.Snapshot
	if s.GPU != nil {
		return ErrInvalidArgument
	}
	wantGPU, err := GPURequestFor(s.ComputeOrigin)
	if err != nil || !reflect.DeepEqual(wantGPU, c.GPURequest) {
		return ErrInvalidArgument
	}
	if c.GPURequest != nil && c.GPURequest.ClusterID != s.Environment.ClusterID {
		return ErrInvalidArgument
	}
	if s.ValidatePrepared() != nil || !reflect.DeepEqual(s.RuntimePlan, RuntimePlan{}) {
		return ErrInvalidArgument
	}
	if s.Kind != c.Intent.Kind || s.Implementation.ImplementationID != c.Intent.ImplementationID || s.ComputeOrigin.ComputePresetID != c.Intent.ComputePresetID || s.Input.InputVersionID != c.Intent.DatasetVersionID || s.Timing.Validate() != nil || !s.DeadlineAt.Equal(s.Timing.ExecutionDeadlineAt) || s.ComputeOrigin.Validate() != nil {
		return ErrInvalidArgument
	}
	if !validUUID(s.IdentityRef.TenantID) || !validUUID(s.IdentityRef.OperationID) || !validUUID(s.IdentityRef.ExecutionID) || !ValidAuditActor(s.IdentityRef.Actor) {
		return ErrInvalidArgument
	}
	definition, ok := Implementations()[s.Implementation.ImplementationID]
	if !ok || definition.Version != s.Implementation.Version || definition.ParameterContractVersion != s.Implementation.ParameterContractVersion || ValidateResolvedParameters(s.Implementation.ImplementationID, s.Program.ResolvedParameters) != nil {
		return ErrInvalidArgument
	}
	if c.Intent.TimeoutSeconds != nil && *c.Intent.TimeoutSeconds != s.Timing.ExecutionTimeoutSeconds {
		return ErrInvalidArgument
	}
	explicit, err := c.Intent.ExplicitParameters()
	if err != nil {
		return err
	}
	for _, p := range explicit {
		found := false
		for _, r := range s.Program.ResolvedParameters {
			if p == r {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalidArgument
		}
	}
	if c.Intent.Kind == "FINETUNING" && (s.Finetuning == nil || c.Intent.BaseModelVersionID == nil || s.Finetuning.BaseModel.VersionID != *c.Intent.BaseModelVersionID || !sameOptionalUUID(c.Intent.TargetModelID, s.Finetuning.TargetModelID) || !sameOptionalString(c.Intent.NewModelName, s.Finetuning.NewModelName) || !templateMatchesIntent(c.Intent, s.Finetuning.TemplatePlan)) {
		return ErrInvalidArgument
	}
	return nil
}

func (c PreparedCandidate) Canonical() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	intent, _, err := CanonicalIntent(c.Intent)
	if err != nil {
		return nil, err
	}
	c.Intent, err = DecodeCanonicalIntent(intent)
	if err != nil {
		return nil, err
	}
	snapshot, err := c.Snapshot.CanonicalPrepared()
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(snapshot, &fields) != nil {
		return nil, ErrInvalidArgument
	}
	// A prepared snapshot has no executable RuntimePlan yet. Omitting the
	// field avoids treating JSON null or a fabricated empty plan as a fact.
	delete(fields, "runtime_plan")
	return encodeCanonicalJSON(struct {
		SchemaVersion string                     `json:"schema_version"`
		Intent        Intent                     `json:"intent"`
		Snapshot      map[string]json.RawMessage `json:"snapshot"`
		GPURequest    *GPURequest                `json:"gpu_request,omitempty"`
	}{c.SchemaVersion, c.Intent, fields, c.GPURequest})
}
func (c PreparedCandidate) Digest() (string, error) {
	b, e := c.Canonical()
	if e != nil {
		return "", e
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]), nil
}
func DecodePreparedCandidate(raw []byte, hash string) (PreparedCandidate, error) {
	var c PreparedCandidate
	if len(raw) == 0 || len(raw) > MaxManifestBytes {
		return c, ErrInvalidArgument
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, ErrInvalidArgument
	}
	canonical, e := c.Canonical()
	if e != nil || !bytes.Equal(raw, canonical) {
		return PreparedCandidate{}, ErrInvalidArgument
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != hash {
		return PreparedCandidate{}, ErrInvalidArgument
	}
	return c, nil
}
