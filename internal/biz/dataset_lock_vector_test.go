package biz

import "testing"

// The Go key and the SQL function must agree byte for byte, or acceptance and
// registration would take different advisory locks and stop excluding each
// other. These vectors are the fixed values both sides were derived to.
func TestDatasetTargetCommitLockKeyMatchesFixedVectors(t *testing.T) {
	for _, tc := range []struct {
		tenant, cluster string
		want            int64
	}{
		{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 3561915907020135758},
		{"ffffffff-ffff-4fff-8fff-ffffffffffff", "00000000-0000-4000-8000-000000000001", 2958339801096088870},
	} {
		if got := DatasetTargetCommitLockKey(tc.tenant, tc.cluster); got != tc.want {
			t.Fatalf("key(%s,%s) = %d, want %d", tc.tenant, tc.cluster, got, tc.want)
		}
	}
}

func TestDatasetTargetCommitLockKeyIsOrderedAndDomainSeparated(t *testing.T) {
	a, b := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	if DatasetTargetCommitLockKey(a, b) == DatasetTargetCommitLockKey(b, a) {
		t.Fatal("tenant and cluster are not ordered in the key")
	}
	// A different domain (the long Ensure lock) must not collide with it: if it
	// did, registration and in-progress Ensure would serialize against each other.
	if DatasetTargetCommitLockKey(a, b) == TenantEnvironmentLockKey(a, b) {
		t.Fatal("dataset target commit lock shares the Ensure session lock key")
	}
}
