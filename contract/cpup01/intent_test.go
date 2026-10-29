package cpup01_test

import (
	"bytes"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"os"
	"strings"
	"testing"
)

func TestIntentStrictSingleCodecAndPresence(t *testing.T) {
	raw := conformance.IntentInputV1()
	i, err := cpup01.ParseIntent(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, omitted, err := cpup01.CanonicalIntent(i)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := cpup01.DecodeCanonicalIntent(canonical)
	if err != nil {
		t.Fatal(err)
	}
	again, hash, _ := cpup01.CanonicalIntent(decoded)
	if !bytes.Equal(again, canonical) || hash != omitted {
		t.Fatal("stored canonical changed")
	}
	v := int64(3)
	i.MLP.Epochs = &v
	_, explicit, _ := cpup01.CanonicalIntent(i)
	if omitted == explicit {
		t.Fatal("omitted and explicit default collapsed")
	}
	for _, replacement := range []string{`"parameters":null`, `"parameters":[]`, `"parameters":{"epochs":0}`, `"parameters":{"epochs":1e1}`, `"parameters":{"seed":4294967296}`, `"parameters":{"learning_rate":"0"}`, `"parameters":{"learning_rate":"0.0000000000000000000000000000001"}`, `"parameters":{"epochs":1,"epochs":2}`, `"parameters":{"env":{}}`, `"parameters":{"epochs":null}`, `"parameters":{},"release_id":"x"`, `"parameters":{},"preset_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"`, `"parameters":{},"contract_version":2`, `"parameters":{},"timeout_seconds":null`, `"parameters":{},"timeout_seconds":1e3`} {
		candidate := strings.Replace(string(raw), `"parameters":{}`, replacement, 1)
		if _, e := cpup01.ParseIntent([]byte(candidate)); e == nil {
			t.Errorf("ambiguous/old request accepted: %s", replacement)
		}
	}
	missing := strings.Replace(string(raw), `,"parameters":{}`, "", 1)
	if _, e := cpup01.ParseIntent([]byte(missing)); e == nil {
		t.Fatal("missing parameters accepted")
	}
}

func TestIndependentPythonGoldenVectors(t *testing.T) {
	i := conformance.IntentV1()
	intent, hash, e := cpup01.CanonicalIntent(i)
	if e != nil {
		t.Fatal(e)
	}
	golden, e := os.ReadFile("conformance/intent-canonical-v3.json")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(intent, golden) || hash != conformance.IntentSHA256V1 {
		t.Fatal("Go intent differs from independent Python golden")
	}
	s := conformance.SnapshotV1()
	snapshot, e := s.Canonical()
	if e != nil {
		t.Fatal(e)
	}
	golden, e = os.ReadFile("conformance/snapshot-v3.json")
	if e != nil {
		t.Fatal(e)
	}
	spec, e := s.Digest()
	if e != nil || !bytes.Equal(snapshot, golden) || spec != conformance.SnapshotSHA256V1 {
		t.Fatal("Go snapshot differs from independent Python golden")
	}
}

func TestIntentUnicodeBoundariesAndNumericSpelling(t *testing.T) {
	raw := string(conformance.IntentInputV1())
	for _, name := range []string{`"\ud800"`, `"\udfff"`, `" a"`, `"a\u0000b"`} {
		if _, e := cpup01.ParseIntent([]byte(strings.Replace(raw, `"CPU training"`, name, 1))); e == nil {
			t.Fatal("invalid Unicode/name accepted", name)
		}
	}
	unicodeName := strings.Replace(raw, `"CPU training"`, `"训练 <&> 😀"`, 1)
	i, e := cpup01.ParseIntent([]byte(unicodeName))
	if e != nil {
		t.Fatal(e)
	}
	canonical, _, _ := cpup01.CanonicalIntent(i)
	if bytes.Contains(canonical, []byte(`\u003c`)) {
		t.Fatal("HTML escaping changed Unicode bytes")
	}
	for _, p := range []string{`{"epochs":9223372036854775808}`, `{"epochs":-1}`, `{"seed":-1}`, `{"learning_rate":"1e-3"}`, `{"learning_rate":"NaN"}`, `{"learning_rate":0.01}`} {
		if _, e := cpup01.ParseIntent([]byte(strings.Replace(raw, `"parameters":{}`, `"parameters":`+p, 1))); e == nil {
			t.Fatal("bad numeric representation accepted", p)
		}
	}
	i, e = cpup01.ParseIntent([]byte(strings.Replace(raw, `"parameters":{}`, `"parameters":{"learning_rate":"1000"}`, 1)))
	if e != nil {
		t.Fatal(e)
	}
	canonical, _, _ = cpup01.CanonicalIntent(i)
	if !bytes.Contains(canonical, []byte(`"learning_rate":"1000"`)) {
		t.Fatal("integer decimal trailing zeros removed")
	}
}

func TestImplementationParametersHaveNoCombinationWhitelist(t *testing.T) {
	raw := strings.Replace(string(conformance.IntentInputV1()), `"parameters":{}`, `"parameters":{"learning_rate":"0.1250","epochs":17,"batch_size":37,"seed":4294967295}`, 1)
	i, e := cpup01.ParseIntent([]byte(raw))
	if e != nil {
		t.Fatal(e)
	}
	p, e := i.ResolveParameters()
	if e != nil || len(p) != 4 {
		t.Fatalf("nonbaseline rejected: %v", e)
	}
	canonical, _, _ := cpup01.CanonicalIntent(i)
	if !bytes.Contains(canonical, []byte(`"learning_rate":"0.125"`)) {
		t.Fatal("decimal was not canonicalized")
	}
}
