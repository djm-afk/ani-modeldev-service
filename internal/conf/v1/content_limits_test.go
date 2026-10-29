package conf

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestRuntimeRequiresOnlyExplicitContentLimit(t *testing.T) {
	for _, value := range []int64{1024, 17179869184} {
		runtime := &ManagedRuntime{Limits: &RuntimeLimits{MaxContentBytes: value}}
		limits, err := runtime.ContentLimits()
		if err != nil || limits.MaxContentBytes != value {
			t.Fatalf("explicit configured limit: %v, %v", limits, err)
		}
	}
	for _, runtime := range []*ManagedRuntime{nil, {}, {Limits: &RuntimeLimits{}}, {Limits: &RuntimeLimits{MaxContentBytes: -1}}} {
		if _, err := runtime.ContentLimits(); err == nil {
			t.Fatal("missing content policy obtained an implicit default")
		}
	}
	if err := protojson.Unmarshal([]byte(`{"max_object_bytes":"17179869184"}`), &ObjectStorageConnection{}); err == nil {
		t.Fatal("old independent storage content policy accepted")
	}
}

func TestRetiredModelBoundaryCannotBeEnabledInRuntimeConfig(t *testing.T) {
	var config GovernanceCommand
	if err := protojson.Unmarshal([]byte(`{"controlled_model_facts":{"files":[]}}`), &config); err == nil {
		t.Fatal("retired file model boundary accepted")
	}
}
