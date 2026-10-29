package cpup01_test

import (
	"encoding/json"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentMaterialRejectsMissingComponentResources(t *testing.T) {
	raw, err := conformance.MaterialV21().Canonical()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "component_resources")
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cpup01.ParseMaterial(raw); err == nil {
		t.Fatal("material without frozen component resources was accepted")
	}
}

func TestCurrentSnapshotRequiresWorkSubpath(t *testing.T) {
	raw := conformance.SnapshotCanonicalV1()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var workspace map[string]json.RawMessage
	if err := json.Unmarshal(document["workspace"], &workspace); err != nil {
		t.Fatal(err)
	}
	delete(workspace, "work_subpath")
	document["workspace"], _ = json.Marshal(workspace)
	raw, _ = json.Marshal(document)
	if _, err := cpup01.DecodeCanonicalSnapshot(raw); err == nil {
		t.Fatal("snapshot without execution work scope was accepted")
	}
}

func TestCurrentComponentsFreezeExactRolesAndPositiveOrderedResources(t *testing.T) {
	for _, kind := range []string{"GENERAL_TRAINING", "FINETUNING"} {
		values := conformance.ComponentResourceFixtures(kind)
		if err := cpup01.ValidateComponentResources(kind, values); err != nil {
			t.Fatal(err)
		}
		for name, change := range map[string]func(map[string]cpup01.ComponentResources){
			"missing":   func(values map[string]cpup01.ComponentResources) { delete(values, "prepare") },
			"primitive": func(values map[string]cpup01.ComponentResources) { values["createpvc"] = values["prepare"] },
			"training":  func(values map[string]cpup01.ComponentResources) { values["train"] = values["prepare"] },
			"zero": func(values map[string]cpup01.ComponentResources) {
				r := values["prepare"]
				r.CPURequest = "0"
				values["prepare"] = r
			},
			"reversed": func(values map[string]cpup01.ComponentResources) {
				r := values["prepare"]
				r.MemoryRequest = "1Gi"
				values["prepare"] = r
			},
			"invalid": func(values map[string]cpup01.ComponentResources) {
				r := values["prepare"]
				r.EphemeralStorageLimit = "garbage"
				values["prepare"] = r
			},
			"missing ephemeral": func(values map[string]cpup01.ComponentResources) {
				r := values["prepare"]
				r.EphemeralStorageRequest = ""
				values["prepare"] = r
			},
		} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				values := conformance.ComponentResourceFixtures(kind)
				change(values)
				if cpup01.ValidateComponentResources(kind, values) == nil {
					t.Fatal("invalid component authority accepted")
				}
			})
		}
	}
}

func TestWorkScopeIsFixedAndSeparateFromOutputs(t *testing.T) {
	for _, value := range []string{"", "scratch", "training", "training/work", "../work"} {
		snapshot := conformance.SnapshotV1()
		snapshot.Workspace.WorkSubpath = value
		if _, err := snapshot.Canonical(); err == nil {
			t.Fatalf("unsafe work scope %q accepted", value)
		}
	}
	snapshot := conformance.SnapshotV1()
	snapshot.Workspace.TrainingSubpath = "work/train"
	if _, err := snapshot.Canonical(); err == nil {
		t.Fatal("overlapping work/output scopes accepted")
	}
}
