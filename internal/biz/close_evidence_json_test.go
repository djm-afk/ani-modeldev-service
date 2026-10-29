package biz_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"testing"
)

// Current close records predate optional CreatePVC observations. Their existing
// canonical bytes remain valid; nil does not assert physical PVC absence.
func TestCurrentCloseEvidenceWithoutUnpreparedWorkspacePreservesCanonicalBytes(t *testing.T) {
	original := []byte(`{"RunID":"beed531c-d16d-47fb-99e3-030e93d089ac","WorkflowUID":"dcac8e3a-7eb9-4417-8a9d-f32722947c07","NoDispatch":false,"Resources":null,"SkippedTasks":null,"ObservedAt":"2026-10-28T22:25:39Z","OwnerTermination":{"RunState":"FAILED","RunFinishedAt":"2026-10-28T22:24:00Z","WorkflowPhase":"Error","WorkflowFinishedAt":"2026-10-28T22:24:00Z","WorkflowResourceVersion":"11957475","WorkflowNodesSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
	var evidence biz.ManagedCloseEvidence
	if err := json.Unmarshal(original, &evidence); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || evidence.UnpreparedWorkspace != nil || !bytes.Equal(encoded, original) || sha256.Sum256(encoded) != sha256.Sum256(original) {
		t.Fatalf("optional observation changed existing current close evidence bytes: %s %v", encoded, err)
	}
}
