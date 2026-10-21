package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
)

// GPUPlan contains the formal ACC execution and display facts. Quota quantities
// remain exclusively in Governance's original ledger and never enter this type.
type GPUPlan struct {
	Execution GPUExecution `json:"execution"`
	Display   GPUDisplay   `json:"display"`
}
type GPUExecution struct {
	SchedulerName    string            `json:"scheduler_name"`
	QueueName        string            `json:"queue_name"`
	RuntimeClassName string            `json:"runtime_class_name"`
	NodeSelector     map[string]string `json:"node_selector"`
	PodAnnotations   map[string]string `json:"pod_annotations"`
	Resources        map[string]string `json:"resources"`
	ExecutionDigest  string            `json:"execution_digest"`
}
type GPUDisplay struct {
	ProfileID      string `json:"profile_id"`
	ProfileVersion uint64 `json:"profile_version,string"`
	ProfileName    string `json:"profile_name"`
	ModelKey       string `json:"model_key"`
	Mode           string `json:"mode"`
}

var gpuLabelValue = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)

func (e GPUExecution) Canonical() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return encodeCanonicalJSON(e)
}
func (d GPUDisplay) Canonical() ([]byte, error) {
	if !validUUID(d.ProfileID) || d.ProfileVersion == 0 || !validSnapshotText(d.ProfileName) || !validSnapshotReference(d.ModelKey) || (d.Mode != "WHOLE_EXCLUSIVE" && d.Mode != "SHARED_FIXED") {
		return nil, ErrInvalidArgument
	}
	return encodeCanonicalJSON(d)
}
func (e GPUExecution) Validate() error {
	bad := func() error { return fmt.Errorf("%w: GPU execution", ErrInvalidArgument) }
	if e.SchedulerName != "volcano" || !validSnapshotDNSName(e.QueueName) || !validSnapshotDNSName(e.RuntimeClassName) || len(e.Resources) != 3 || (len(e.NodeSelector) != 3 && len(e.NodeSelector) != 4) || len(e.PodAnnotations) != 1 || e.PodAnnotations["volcano.sh/vgpu-mode"] != "hami-core" {
		return bad()
	}
	for _, k := range []string{"accelerator.ani.io/supply-group", "accelerator.ani.io/model-key", "accelerator.ani.io/baseline-id"} {
		if !gpuLabelValue.MatchString(e.NodeSelector[k]) || len(e.NodeSelector[k]) > 63 {
			return bad()
		}
	}
	if hostname, exists := e.NodeSelector["kubernetes.io/hostname"]; exists {
		if !gpuLabelValue.MatchString(hostname) || len(hostname) > 63 {
			return bad()
		}
	} else if len(e.NodeSelector) != 3 {
		return bad()
	}
	for _, k := range []string{"volcano.sh/vgpu-number", "volcano.sh/vgpu-memory", "volcano.sh/vgpu-cores"} {
		s := e.Resources[k]
		v, err := strconv.ParseInt(s, 10, 32)
		if err != nil || v <= 0 || strconv.FormatInt(v, 10) != s || k == "volcano.sh/vgpu-cores" && v > 100 {
			return bad()
		}
	}
	fields := map[string]any{"scheduler_name": e.SchedulerName, "queue_name": e.QueueName, "runtime_class_name": e.RuntimeClassName, "node_selector": e.NodeSelector, "pod_annotations": e.PodAnnotations, "resources": e.Resources}
	raw, err := encodeCanonicalJSON(fields)
	if err != nil {
		return bad()
	}
	sum := sha256.Sum256(append([]byte("acc-execution-v1\n"), raw...))
	if hex.EncodeToString(sum[:]) != e.ExecutionDigest {
		return bad()
	}
	return nil
}
func (p GPUPlan) ValidateFor(origin ComputeOrigin) error {
	request, err := GPURequestFor(origin)
	if err != nil || request == nil || p.Execution.Validate() != nil {
		return ErrInvalidArgument
	}
	if _, err := p.Display.Canonical(); err != nil {
		return err
	}
	d, e := p.Display, p.Execution
	if d.ProfileID != request.ProfileID || d.ProfileVersion != request.ProfileVersion || d.Mode != request.Mode || d.ModelKey != request.ModelKey || e.NodeSelector["accelerator.ani.io/model-key"] != d.ModelKey || e.Resources["volcano.sh/vgpu-number"] != "1" || e.Resources["volcano.sh/vgpu-memory"] != strconv.FormatInt(request.MemoryMiBPerDevice, 10) || e.Resources["volcano.sh/vgpu-cores"] != strconv.FormatUint(uint64(request.CoreLimitPercent), 10) {
		return ErrInvalidArgument
	}
	return nil
}
