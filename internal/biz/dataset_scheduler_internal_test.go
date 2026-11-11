package biz

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const schedulerTenant = "11111111-1111-4111-8111-111111111111"

func schedulerTask() DatasetImportTask {
	return DatasetImportTask{
		TenantID: schedulerTenant, TaskID: "22222222-2222-4222-8222-222222222222",
		DatasetID:              "33333333-3333-4333-8333-333333333333",
		ReservedInputVersionID: "44444444-4444-4444-8444-444444444444",
		InputRequestID:         "55555555-5555-4555-8555-555555555555",
		VersionNumber:          1, AcceptedAt: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC),
		SourceProvider: "MODELSCOPE", SourceSnapshotSHA256: fillerHex('a', 64),
		SourceSnapshot:    []byte(`{"schema":"ani.modeldev.dataset.source-snapshot.v1","provider":"MODELSCOPE","repo_type":"dataset","repo_id":"org/ds","requested_revision":"master","file_path":"data.json","lock_kind":"FILE_SHA256","resolved_revision":"master","expected_size_bytes":"116","expected_file_sha256":"` + fillerHex('b', 64) + `","resolved_at":"2026-11-04T09:00:00.000000Z"}`),
		ExpectedSizeBytes: 116, AcceptedLimitBytes: 1 << 30,
		StorageConnectionID: "store-1", Bucket: "inputs-bucket",
		ApprovedPrefix: "inputs/" + schedulerTenant, InputScopeSHA256: fillerHex('c', 64),
		CredentialReference: "cred-1", ClusterID: "66666666-6666-4666-8666-666666666666",
		NamespaceName: "tenant-" + schedulerTenant, NamespaceUID: "77777777-7777-4777-8777-777777777777",
		State: TaskQueued, Phase: PhaseWaiting, RecoveryGeneration: 1,
	}
}

func fillerHex(r byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

// The plan must be decodable by the importer's strict decoder shape: fixed
// schema, nested data/result objects and a microsecond deadline.
func TestImportPlanEncodingIsStableAndNested(t *testing.T) {
	planner, err := NewImportPlanPlanner(fillerHex('d', 64), "/var/run/credentials/credentials.json", 30*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := schedulerTask()
	first, err := planner.PlanAttempt(context.Background(), task, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewImportPlanPlanner(fillerHex('d', 64), "/var/run/credentials/credentials.json", 30*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := second.PlanAttempt(context.Background(), task, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.RuntimePlan) != string(again.RuntimePlan) || first.RuntimePlanSHA256 != again.RuntimePlanSHA256 {
		t.Fatal("plan is not deterministic for the same frozen task")
	}
	var decoded map[string]any
	if err := json.Unmarshal(first.RuntimePlan, &decoded); err != nil {
		t.Fatalf("plan is not valid JSON: %v", err)
	}
	for _, key := range []string{"schema", "tenant_id", "dataset_id", "task_id", "input_version_id", "source_snapshot_sha256", "runtime_plan_sha256", "importer_image_digest", "provider", "repo_id", "requested_revision", "file_path", "lock_kind", "expected_size_bytes", "credential_file", "deadline", "data", "result"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("plan is missing %q", key)
		}
	}
	// input_scope-style dotted keys were a real bug once; the same mistake here
	// would silently produce a plan the importer cannot decode.
	for key := range decoded {
		if len(key) > 7 && key[:8] == "data.key" {
			t.Fatalf("dotted key leaked into the plan: %q", key)
		}
	}
	data, ok := decoded["data"].(map[string]any)
	if !ok || data["key"] == "" || data["bucket"] == "" {
		t.Fatalf("data object is not nested: %#v", decoded["data"])
	}
	// The importer rejects a plan without an attempt id, so the encoder must
	// carry the allocated attempt rather than an empty placeholder.
	if decoded["attempt_id"] != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("plan attempt_id is %v", decoded["attempt_id"])
	}
	if first.DataObjectKey != data["key"] {
		t.Fatalf("frozen key differs from the encoded plan: %q vs %v", first.DataObjectKey, data["key"])
	}
	// The digest covers the plan without itself.
	if decoded["runtime_plan_sha256"] != first.RuntimePlanSHA256 {
		t.Fatal("runtime_plan_sha256 does not match the recorded digest")
	}
}

// The object layout is per attempt, so a retry never overwrites the previous
// attempt's bytes.
func TestImportObjectKeysArePerAttempt(t *testing.T) {
	task := schedulerTask()
	one, err := ImporterDataObjectKey(task, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ImporterDataObjectKey(task, 2)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("attempts share a data key")
	}
	result, err := ImporterResultObjectKey(task, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result == one || result == two {
		t.Fatal("result key collides with a data key")
	}
	// A prefix that is not this tenant's is not a legal layout.
	foreign := task
	foreign.ApprovedPrefix = "inputs/99999999-9999-4999-8999-999999999999"
	if _, err := ImporterDataObjectKey(foreign, 1); err == nil {
		t.Fatal("foreign tenant prefix accepted into the object layout")
	}
}

// A content verdict must never be treated as retryable: re-downloading the same
// bytes cannot fix an invalid file.
func TestSchedulerClassifiesRetryability(t *testing.T) {
	scheduler := &ImportScheduler{interval: time.Second}
	for _, cause := range []error{ErrImportDispatchUnavailable, ErrImportQuotaExhausted, ErrSourceUnavailable, ErrTaskLeaseLost} {
		if !scheduler.retryable(cause) {
			t.Fatalf("%v should be retryable", cause)
		}
	}
	for _, cause := range []error{ErrInputVerification, ErrSourceChanged, ErrSourceNotFound, ErrDatasetInvalidMetadata} {
		if scheduler.retryable(cause) {
			t.Fatalf("%v must not be retryable", cause)
		}
	}
	if failureCodeFor(ErrSourceChanged) != string(FailSourceChanged) {
		t.Fatal("a changed source must report SOURCE_CHANGED")
	}
}

// A report's own FAILED verdict must survive classification: the job is the only
// party that observed what went wrong, so its code is the one the page branches
// on. Rewriting it to a generic persistence error erases the reason.
func TestFailureCodeKeepsTheReportedVerdict(t *testing.T) {
	for _, code := range []ImportFailureCode{FailSourceChanged, FailSourceNotFound, FailSourceAuthRequired,
		FailDependencyUnavailable, FailResourceUnavailable, FailTimeout, FailStorageVersioningRequired} {
		if got := failureCodeFor(errors.New(string(code))); got != string(code) {
			t.Fatalf("reported %s became %s", code, got)
		}
	}
	if got := failureCodeFor(ErrInputVerification); got != string(FailDatasetInvalid) {
		t.Fatalf("a content rejection became %s, want %s", got, FailDatasetInvalid)
	}
	if got := failureCodeFor(ErrImportResultInvalid); got != string(FailImportResultInvalid) {
		t.Fatalf("an unreadable report became %s, want %s", got, FailImportResultInvalid)
	}
	if got := failureCodeFor(errors.New("something else")); got != string(FailPersistence) {
		t.Fatalf("an unknown cause became %s, want %s", got, FailPersistence)
	}
}

// The scheduler must refuse an assembly missing any port rather than run a
// half-wired loop.
func TestSchedulerRequiresEveryPort(t *testing.T) {
	if _, err := NewImportScheduler(ImportSchedulerDeps{}); !errors.Is(err, ErrImportWorkerConfiguration) {
		t.Fatalf("empty assembly accepted: %v", err)
	}
}
