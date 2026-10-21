package protobuf

import (
	"bytes"
	"encoding/json"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	computev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func DecodeComputeSelection(canonical []byte, digest string) (cpup01.ComputeOrigin, error) {
	var selection computev1.ComputeSelection
	if len(canonical) == 0 || len(canonical) > 16384 || protojson.Unmarshal(canonical, &selection) != nil {
		return cpup01.ComputeOrigin{}, invalid("compute selection")
	}
	selection.SelectionDigest = digest
	if computev1.ValidateSelection(&selection) != nil {
		return cpup01.ComputeOrigin{}, invalid("compute selection")
	}
	exact, err := computev1.CanonicalSelection(&selection)
	if err != nil || !bytes.Equal(exact, canonical) {
		return cpup01.ComputeOrigin{}, invalid("compute selection canonical")
	}
	origin := cpup01.ComputeOrigin{ComputePresetID: selection.PresetId, SelectionDigest: selection.SelectionDigest, SelectionJSON: string(exact), CPUMillis: selection.CpuMillis, MemoryMiB: selection.MemoryMib, DeviceMode: selection.ComputeType}
	if origin.Validate() != nil {
		return cpup01.ComputeOrigin{}, invalid("compute origin")
	}
	return origin, nil
}
func encodeIdentity(v cpup01.IdentityRef) *modeldevv1.IdentityRef {
	return &modeldevv1.IdentityRef{TenantId: v.TenantID, Actor: v.Actor, ExecutionId: v.ExecutionID, OperationId: v.OperationID, IntentHash: v.IntentHash}
}
func decodeIdentity(v *modeldevv1.IdentityRef) cpup01.IdentityRef {
	return cpup01.IdentityRef{TenantID: v.TenantId, Actor: v.Actor, ExecutionID: v.ExecutionId, OperationID: v.OperationId, IntentHash: v.IntentHash}
}
func encodeImplementation(v cpup01.ImplementationSnapshot) *modeldevv1.ImplementationSnapshot {
	return &modeldevv1.ImplementationSnapshot{ImplementationId: v.ImplementationID, Version: v.Version, ParameterContractVersion: v.ParameterContractVersion, PipelineId: v.PipelineID, PipelineVersionId: v.PipelineVersionID, PipelineIrSha256: v.PipelineIRSHA256}
}
func decodeImplementation(v *modeldevv1.ImplementationSnapshot) cpup01.ImplementationSnapshot {
	return cpup01.ImplementationSnapshot{ImplementationID: v.ImplementationId, Version: v.Version, ParameterContractVersion: v.ParameterContractVersion, PipelineID: v.PipelineId, PipelineVersionID: v.PipelineVersionId, PipelineIRSHA256: v.PipelineIrSha256}
}
func encodeComputeOrigin(v cpup01.ComputeOrigin) *modeldevv1.ComputeOrigin {
	return &modeldevv1.ComputeOrigin{ComputePresetId: v.ComputePresetID, SelectionDigest: v.SelectionDigest, SelectionCanonical: []byte(v.SelectionJSON), CpuMillis: v.CPUMillis, MemoryMib: v.MemoryMiB, DeviceMode: v.DeviceMode}
}
func decodeComputeOrigin(v *modeldevv1.ComputeOrigin) cpup01.ComputeOrigin {
	return cpup01.ComputeOrigin{ComputePresetID: v.ComputePresetId, SelectionDigest: v.SelectionDigest, SelectionJSON: string(v.SelectionCanonical), CPUMillis: v.CpuMillis, MemoryMiB: v.MemoryMib, DeviceMode: v.DeviceMode}
}
func encodeTiming(v cpup01.Timing) *modeldevv1.ExecutionTiming {
	return &modeldevv1.ExecutionTiming{AcceptedAt: timestamppb.New(v.AcceptedAt), ExecutionTimeoutSeconds: v.ExecutionTimeoutSeconds, ExecutionTimeoutMaxSeconds: v.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: v.StepTimeoutSeconds, ExecutionDeadlineAt: timestamppb.New(v.ExecutionDeadlineAt)}
}
func decodeTiming(v *modeldevv1.ExecutionTiming) cpup01.Timing {
	return cpup01.Timing{AcceptedAt: v.AcceptedAt.AsTime(), ExecutionTimeoutSeconds: v.ExecutionTimeoutSeconds, ExecutionTimeoutMaxSeconds: v.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: v.StepTimeoutSeconds, ExecutionDeadlineAt: v.ExecutionDeadlineAt.AsTime()}
}
func encodeRuntimePlan(v cpup01.RuntimePlan) *modeldevv1.RuntimePlan {
	return &modeldevv1.RuntimePlan{GeneratorVersion: v.GeneratorVersion, ApiGroup: v.APIGroup, Kind: v.Kind, Namespace: v.Namespace, Name: v.Name, ExpectedSpecCanonical: append([]byte{}, v.ExpectedSpec...), ExpectedSpecSha256: v.ExpectedSpecSHA256, DefaultingRules: v.DefaultingRules, TargetJobs: cloneStrings(v.TargetJobs)}
}
func decodeRuntimePlan(v *modeldevv1.RuntimePlan) cpup01.RuntimePlan {
	return cpup01.RuntimePlan{GeneratorVersion: v.GeneratorVersion, APIGroup: v.ApiGroup, Kind: v.Kind, Namespace: v.Namespace, Name: v.Name, ExpectedSpec: append(json.RawMessage{}, v.ExpectedSpecCanonical...), ExpectedSpecSHA256: v.ExpectedSpecSha256, DefaultingRules: v.DefaultingRules, TargetJobs: cloneStrings(v.TargetJobs)}
}
