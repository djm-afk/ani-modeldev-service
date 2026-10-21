package protobuf

import (
	"bytes"
	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"google.golang.org/protobuf/encoding/protojson"
	"sort"
)

func copyStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}
func gpuDomain(e *accpb.GpuExecution, d *accpb.GpuDisplay) (*cpup01.GPUPlan, error) {
	if gpuprotocol.ValidateGpuExecution(e) != nil || d == nil || len(d.ProtoReflect().GetUnknown()) != 0 {
		return nil, invalid("formal accelerator facts")
	}
	result := &cpup01.GPUPlan{Execution: cpup01.GPUExecution{SchedulerName: e.SchedulerName, QueueName: e.QueueName, RuntimeClassName: e.RuntimeClassName, NodeSelector: copyStringMap(e.NodeSelector), PodAnnotations: copyStringMap(e.PodAnnotations), Resources: copyStringMap(e.Resources), ExecutionDigest: e.ExecutionDigest}, Display: cpup01.GPUDisplay{ProfileID: d.ProfileId, ProfileVersion: d.ProfileVersion, ProfileName: d.ProfileName, ModelKey: d.ModelKey, Mode: d.Mode.String()}}
	if result.Execution.Validate() != nil {
		return nil, invalid("GPU execution")
	}
	if _, err := result.Display.Canonical(); err != nil {
		return nil, invalid("GPU display")
	}
	return result, nil
}

// EncodeGPUPlanCanonical is shared with Governance. The authoritative ACC
// helper verifies its actual message before the pure snapshot facts are encoded.
func EncodeGPUPlanCanonical(e *accpb.GpuExecution, d *accpb.GpuDisplay) ([]byte, []byte, error) {
	plan, err := gpuDomain(e, d)
	if err != nil {
		return nil, nil, err
	}
	execution, err := plan.Execution.Canonical()
	if err != nil {
		return nil, nil, err
	}
	display, err := plan.Display.Canonical()
	if err != nil {
		return nil, nil, err
	}
	return execution, display, nil
}
func DecodeGPUPlanCanonical(execution, display []byte) (*cpup01.GPUPlan, error) {
	if len(execution) == 0 || len(execution) > 16384 || len(display) == 0 || len(display) > 4096 {
		return nil, invalid("GPU facts size")
	}
	var e accpb.GpuExecution
	var d accpb.GpuDisplay
	if protojson.Unmarshal(execution, &e) != nil || protojson.Unmarshal(display, &d) != nil {
		return nil, invalid("GPU facts JSON")
	}
	exactExecution, exactDisplay, err := EncodeGPUPlanCanonical(&e, &d)
	if err != nil || !bytes.Equal(exactExecution, execution) || !bytes.Equal(exactDisplay, display) {
		return nil, invalid("GPU canonical facts")
	}
	return gpuDomain(&e, &d)
}
func sortedPairs(values map[string]string) []*modeldevv1.StringPair {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*modeldevv1.StringPair, 0, len(keys))
	for _, k := range keys {
		out = append(out, &modeldevv1.StringPair{Key: k, Value: values[k]})
	}
	return out
}
func decodePairs(values []*modeldevv1.StringPair) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for _, v := range values {
		if v == nil || v.Key == "" {
			return nil, invalid("GPU map")
		}
		if _, exists := out[v.Key]; exists {
			return nil, invalid("duplicate GPU map entry")
		}
		out[v.Key] = v.Value
	}
	return out, nil
}
func encodeGPUPlan(p *cpup01.GPUPlan) *modeldevv1.GPUPlan {
	e, d := p.Execution, p.Display
	return &modeldevv1.GPUPlan{Execution: &modeldevv1.GPUExecutionSnapshot{SchedulerName: e.SchedulerName, QueueName: e.QueueName, RuntimeClassName: e.RuntimeClassName, NodeSelector: sortedPairs(e.NodeSelector), PodAnnotations: sortedPairs(e.PodAnnotations), Resources: sortedPairs(e.Resources), ExecutionDigest: e.ExecutionDigest}, Display: &modeldevv1.GPUDisplaySnapshot{ProfileId: d.ProfileID, ProfileVersion: d.ProfileVersion, ProfileName: d.ProfileName, ModelKey: d.ModelKey, Mode: d.Mode}}
}
func decodeGPUPlan(m *modeldevv1.GPUPlan) (*cpup01.GPUPlan, error) {
	if m == nil || m.Execution == nil || m.Display == nil {
		return nil, invalid("GPU plan")
	}
	e, d := m.Execution, m.Display
	selectors, err := decodePairs(e.NodeSelector)
	if err != nil {
		return nil, err
	}
	annotations, err := decodePairs(e.PodAnnotations)
	if err != nil {
		return nil, err
	}
	resources, err := decodePairs(e.Resources)
	if err != nil {
		return nil, err
	}
	formalE := &accpb.GpuExecution{SchedulerName: e.SchedulerName, QueueName: e.QueueName, RuntimeClassName: e.RuntimeClassName, NodeSelector: selectors, PodAnnotations: annotations, Resources: resources, ExecutionDigest: e.ExecutionDigest}
	formalD := &accpb.GpuDisplay{ProfileId: d.ProfileId, ProfileVersion: d.ProfileVersion, ProfileName: d.ProfileName, ModelKey: d.ModelKey, Mode: accpb.SupplyMode(accpb.SupplyMode_value[d.Mode])}
	return gpuDomain(formalE, formalD)
}
