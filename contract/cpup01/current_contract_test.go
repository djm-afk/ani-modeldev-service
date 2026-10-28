package cpup01_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentIntentFreezesExplicitTemplateInCanonicalIdentity(t *testing.T) {
	original := conformance.FinetuningIntentV1()
	plain, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(plain, &request); err != nil {
		t.Fatal(err)
	}
	request["finetuning_template"] = "native_model_template"
	explicit, _ := json.Marshal(request)
	parsed, err := cpup01.ParseIntent(explicit)
	if err != nil {
		t.Fatalf("explicit current template: %v", err)
	}
	canonical, hash, err := cpup01.CanonicalIntent(parsed)
	if err != nil || !bytes.Contains(canonical, []byte(`"schema":"ani.modeldev.intent.v3"`)) || !bytes.Contains(canonical, []byte(`"finetuning_template":"native_model_template"`)) {
		t.Fatalf("current canonical intent: %s, %v", canonical, err)
	}
	_, autoHash, _ := cpup01.CanonicalIntent(original)
	if hash == autoHash {
		t.Fatal("explicit template changes the idempotent intent")
	}
	if _, err := cpup01.DecodeCanonicalIntent(canonical); err != nil {
		t.Fatalf("stored current intent: %v", err)
	}
	if _, err := cpup01.DecodeCanonicalIntent(bytes.Replace(canonical, []byte("ani.modeldev.intent.v3"), []byte("ani.modeldev.intent.v2.1"), 1)); err == nil {
		t.Fatal("old stored schema accepted")
	}
	request["finetuning_template"] = ""
	empty, _ := json.Marshal(request)
	if _, err := cpup01.ParseIntent(empty); err == nil {
		t.Fatal("explicit empty template accepted as automatic selection")
	}
}
