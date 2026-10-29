package cpup01_test

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"testing"
)

func TestCurrentRuntimeContractRejectsMissingOrDifferentVolcanoPolicy(t *testing.T) {
	for _, spec := range []string{`{"template":{}}`, `{"podGroupPolicy":{},"template":{}}`, `{"podGroupPolicy":{"other":{}},"template":{}}`, `{"podGroupPolicy":{"volcano":{"minMember":1}},"template":{}}`, `{"podGroupPolicy":{"volcano":{},"other":{}},"template":{}}`} {
		plan := conformance.SnapshotV1().RuntimePlan
		plan.ExpectedSpec = []byte(spec)
		digest := sha256.Sum256(plan.ExpectedSpec)
		plan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
		if plan.Validate() == nil {
			t.Fatalf("retired/different policy accepted: %s", spec)
		}
	}
}
