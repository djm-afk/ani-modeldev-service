package cpup01_test

import (
	"bytes"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentSnapshotRetainsItsFrozenContentLimit(t *testing.T) {
	snapshot := conformance.SnapshotV1()
	snapshot.Limits = cpup01.ContentLimits{MaxContentBytes: 17179869184}
	snapshot.Input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: snapshot.Input.Object.SizeBytes, SHA256: snapshot.Input.Object.SHA256, RowCount: snapshot.Input.RowCount}
	raw, err := snapshot.Canonical()
	if err != nil || !bytes.Contains(raw, []byte(`"max_content_bytes":"17179869184"`)) {
		t.Fatalf("frozen limit canonical: %s, %v", raw, err)
	}
	newPolicy := cpup01.ContentLimits{MaxContentBytes: 1}
	if newPolicy.Check(cpup01.ContentDataset, snapshot.Input.Object.SizeBytes) == nil {
		t.Fatal("new Prepare policy should reject this source")
	}
	recovered, err := cpup01.DecodeCanonicalSnapshot(raw)
	if err != nil || recovered.Limits != snapshot.Limits {
		t.Fatalf("frozen decode must not consult new admission policy: %#v, %v", recovered.Limits, err)
	}
	recovered.Limits.MaxContentBytes = 0
	if recovered.Validate() == nil {
		t.Fatal("current snapshot missing frozen L accepted")
	}
}
