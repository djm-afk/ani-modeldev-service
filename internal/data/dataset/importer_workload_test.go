package dataset

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/api/resource"
)

// The staging volume must fit the file the task will download. A fixed size
// silently capped every import at whatever the deployment configured, so a file
// the platform accepted could never be staged.
func TestPVCStorageGrowsWithTheFile(t *testing.T) {
	workload := &ImporterWorkload{pvcSize: "1Gi"}
	const mib = int64(1 << 20)

	// A small file keeps the configured floor.
	if got, err := workload.pvcStorage(4 * mib); err != nil || got != "1Gi" {
		t.Fatalf("small file: got %q err=%v, want the configured floor", got, err)
	}
	// A file larger than the floor gets its own size plus headroom.
	got, err := workload.pvcStorage(4 << 30)
	if err != nil {
		t.Fatalf("large file: %v", err)
	}
	requested, err := resource.ParseQuantity(got)
	if err != nil {
		t.Fatalf("large file: %q is not a quantity: %v", got, err)
	}
	// ceil(4Gi * 1.2) + 256MiB, so the request must exceed 5Gi and stay under 6Gi.
	if requested.Cmp(resource.MustParse("5Gi")) <= 0 || requested.Cmp(resource.MustParse("6Gi")) >= 0 {
		t.Fatalf("large file: got %q, want between 5Gi and 6Gi", got)
	}
}

// A malformed configured floor is a deployment fault, not a per-task failure.
func TestPVCStorageRejectsAMalformedFloor(t *testing.T) {
	workload := &ImporterWorkload{pvcSize: "not-a-quantity"}
	if _, err := workload.pvcStorage(1 << 20); err != biz.ErrImportWorkerConfiguration {
		t.Fatalf("malformed floor returned %v", err)
	}
}
