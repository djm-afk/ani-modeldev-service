package input_test

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"reflect"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestInputVerificationBecomesReadyOnlyAfterMatchingProofIsDurable(t *testing.T) {
	openPool := postgres.Prepare(t)
	writer := openPool()
	repository := input.New(writer)
	request := importFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frozen, err := repository.FreezeImport(ctx, request)
	if err != nil {
		t.Fatalf("freeze import before verification: %v", err)
	}
	if frozen.State != biz.InputStateValidating || frozen.Verification != nil {
		t.Fatalf("unverified import exposes a readiness proof: %+v", frozen)
	}
	// Synthetic trusted observation exercises this repository's real durable
	// transition. Actual byte/CSV verification has separate SDK boundary tests.
	proof := biz.VerifiedCSV{
		VerifiedObject: biz.VerifiedObject{Object: request.Object, VerifiedAt: request.RequestedAt.Add(time.Minute)},
		SchemaVersion:  "ani.cpu.csv.v1", RowCount: 1024, FeatureCount: 16,
		PreparedDataset: cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: request.Object.SizeBytes, SHA256: request.Object.SHA256, RowCount: 1024},
	}
	ready, err := repository.RecordVerifiedCSV(ctx, request, proof)
	if err != nil {
		t.Fatalf("persist matching verification before returning READY: %v", err)
	}
	if ready.State != biz.InputStateReady || !reflect.DeepEqual(ready.Import, frozen.Import) || ready.Verification == nil || !reflect.DeepEqual(*ready.Verification, proof) {
		t.Fatalf("READY lost fixed import or actual verification facts: %+v", ready)
	}
	writer.Close()
	recovered, err := input.New(openPool()).Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || !reflect.DeepEqual(recovered, ready) {
		t.Fatalf("fresh connection cannot recover verified input: %+v, %v", recovered, err)
	}
}
