package biz_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestRuntimePlanCompilesCompleteSingleWorkerFromTheSameResourceVector(t *testing.T) {
	for _, v := range []struct {
		cpu, memory         int64
		wantCPU, wantMemory string
	}{{500, 512 << 20, "500m", "512Mi"}, {1000, 1 << 30, "1", "1Gi"}} {
		s := conformance.SnapshotV1()
		s.Resources.RequestMillicpu, s.Resources.LimitMillicpu = v.cpu, v.cpu
		s.Resources.RequestMemoryBytes, s.Resources.LimitMemoryBytes = v.memory, v.memory
		s.ComputeOrigin.CPUMillis, s.ComputeOrigin.MemoryMiB = v.cpu, v.memory/(1<<20)
		s.ComputeOrigin.SelectionJSON = strings.Replace(s.ComputeOrigin.SelectionJSON, "\"cpu_millis\":1000", "\"cpu_millis\":"+strconv.FormatInt(v.cpu, 10), 1)
		s.ComputeOrigin.SelectionJSON = strings.Replace(s.ComputeOrigin.SelectionJSON, "\"memory_mib\":1024", "\"memory_mib\":"+strconv.FormatInt(v.memory/(1<<20), 10), 1)
		digest := sha256.Sum256([]byte("compute-selection-v1\n" + s.ComputeOrigin.SelectionJSON))
		s.ComputeOrigin.SelectionDigest = hex.EncodeToString(digest[:])
		plan, err := biz.CompileRuntimePlan(s)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Name != s.RuntimePlan.Name || plan.Namespace != s.Environment.NamespaceName || plan.Kind != "TrainingRuntime" || len(plan.Name) != 53 {
			t.Fatalf("incorrect per-execution identity: %+v", plan)
		}
		var spec struct {
			Template struct {
				Spec struct {
					Jobs []struct {
						Template struct {
							Spec struct {
								Deadline uint64 `json:"activeDeadlineSeconds"`
								Template struct {
									Metadata struct {
										Finalizers []string `json:"finalizers"`
									} `json:"metadata"`
									Spec struct {
										Account    string `json:"serviceAccountName"`
										Containers []struct {
											Name      string                                       `json:"name"`
											Resources struct{ Requests, Limits map[string]string } `json:"resources"`
										} `json:"containers"`
									} `json:"spec"`
								} `json:"template"`
							} `json:"spec"`
						} `json:"template"`
					} `json:"replicatedJobs"`
				} `json:"spec"`
			} `json:"template"`
		}
		if err := json.Unmarshal(plan.ExpectedSpec, &spec); err != nil {
			t.Fatal(err)
		}
		if len(spec.Template.Spec.Jobs) != 1 {
			t.Fatal("multiple training jobs")
		}
		job := spec.Template.Spec.Jobs[0].Template.Spec
		pod := job.Template.Spec
		if job.Deadline != s.Timing.StepTimeoutSeconds || pod.Account != s.Environment.Identities.TrainerServiceAccount || len(pod.Containers) != 1 || pod.Containers[0].Name != "node" || len(job.Template.Metadata.Finalizers) != 1 {
			t.Fatal("missing frozen timing, identity or retention")
		}
		vector, err := biz.TrainingResourceVector(s)
		container := pod.Containers[0]
		if err != nil || vector["cpu"] != v.wantCPU || vector["memory"] != v.wantMemory || container.Resources.Requests["cpu"] != vector["cpu"] || container.Resources.Limits["cpu"] != vector["cpu"] || container.Resources.Requests["memory"] != vector["memory"] || container.Resources.Limits["memory"] != vector["memory"] {
			t.Fatal("Runtime and TrainJob vector diverged")
		}
		if !strings.Contains(string(plan.ExpectedSpec), biz.ExpectedWorkspacePVCName(s.IdentityRef.ExecutionID)) || strings.Contains(string(plan.ExpectedSpec), "execution-spec-sha256") {
			t.Fatal("workspace was not frozen or digest became self-referential")
		}
		s.RuntimePlan = plan
		if _, err := s.Digest(); err != nil {
			t.Fatal("complete snapshot rejected compiled plan", err)
		}
	}
}

func TestRuntimePlanNeverSharesBetweenExecutionsOrSilentlyAcceptsResourceDrift(t *testing.T) {
	s := conformance.SnapshotV1()
	first, err := biz.CompileRuntimePlan(s)
	if err != nil {
		t.Fatal(err)
	}
	s.IdentityRef.ExecutionID = "11111111-1111-4111-8111-111111111111"
	second, err := biz.CompileRuntimePlan(s)
	if err != nil || first.Name == second.Name || first.ExpectedSpecSHA256 == second.ExpectedSpecSHA256 {
		t.Fatal("executions shared a Runtime/PVC")
	}
	s.Resources.LimitMillicpu++
	if _, err := biz.CompileRuntimePlan(s); err == nil {
		t.Fatal("accepted unequal resource vector")
	}
	s.Resources.LimitMillicpu--
	s.ComputeOrigin.DeviceMode = "gpu"
	if _, err := biz.CompileRuntimePlan(s); err == nil {
		t.Fatal("GPU selected without a formal execution")
	}
}
