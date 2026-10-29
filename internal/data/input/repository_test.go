package input_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestFreezeImportPersistsExactObjectBeforeValidationAcrossNewConnections(t *testing.T) {
	openPool := postgres.Prepare(t)
	writer := openPool()
	command := importFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := input.New(writer).FreezeImport(ctx, command)
	if err != nil {
		t.Fatalf("persist a fixed import before validation: %v", err)
	}
	if got.State != biz.InputStateValidating || !reflect.DeepEqual(got.Import, command) {
		t.Fatalf("receipt lost fixed input facts or falsely became READY: %+v", got)
	}
	writer.Close()
	reader := input.New(openPool())
	recovered, err := reader.Get(ctx, command.TenantID, command.InputVersionID)
	if err != nil || !reflect.DeepEqual(recovered, got) {
		t.Fatalf("new connection did not recover original durable import: %+v, %v", recovered, err)
	}
}

func importFixture() biz.InputImport {
	version := "fixture-source-version-1"
	return biz.InputImport{
		Format:         "CSV",
		TenantID:       "11111111-2222-4333-8444-555555555555",
		RequestID:      "22222222-2222-4222-8222-222222222222",
		InputVersionID: "33333333-3333-4333-8333-333333333333",
		Actor:          "governance:user:42", RequestedAt: time.Date(2026, 9, 30, 12, 0, 0, 123000, time.UTC),
		Scope:  cpup01.StorageScope{StorageConnectionID: "cpu-input-fixture", Bucket: "cpu-inputs", ApprovedPrefix: "tenant/input"},
		Object: cpup01.FixedObjectRef{StorageConnectionID: "cpu-input-fixture", Bucket: "cpu-inputs", Key: "tenant/input/data.csv", VersionID: &version, SizeBytes: 65536, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
}
