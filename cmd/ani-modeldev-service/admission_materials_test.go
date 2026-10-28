package main

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAdmissionMaterialsOmittedKeepsDurableCommandsWithoutAdmission(t *testing.T) {
	config, openPool, certificates, applicationName := commandAppFixture(t)
	if config.Command.AdmissionResolution != nil {
		t.Fatal("ADMISSION_MATERIAL_PREFLIGHT: command-only fixture unexpectedly configured admission; behavior NOT_RUN")
	}
	fixture := configuredAdmissionFixture{config: config, openPool: openPool, certificates: certificates, applicationName: applicationName}
	connection, _ := startConfiguredAdmissionApp(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tenantID := uuid.NewString()
	rpcContext := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-ani-tenant-id", tenantID, "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(),
	))
	request := &modeldevv1.ApplyCloseIntentRequest{
		Identity: &trainingv1.ExecutionIdentity{
			OperationId: uuid.NewString(), ExecutionId: uuid.NewString(), ExecutionSpecHash: strings.Repeat("b", 64),
		},
		ResourceTenantId: tenantID, IntentGeneration: 41, Reason: modeldevv1.CloseReason_CLOSE_REASON_USER_STOP,
		RequestedAt: timestamppb.New(time.Now().UTC().Truncate(time.Microsecond)), RequestedActorId: "governance:user:42",
	}
	receipt, err := modeldevv1.NewModelDevCommandServiceClient(connection).ApplyCloseIntent(rpcContext, request)
	if err != nil || receipt == nil || !receipt.DurablyRecorded || receipt.Replayed || receipt.CloseGeneration != 1 {
		t.Fatalf("ADMISSION_MATERIAL_BEHAVIOR: omitting admission disabled durable commands (code=%s)", status.Code(err))
	}
	stored, err := execution.New(openPool()).GetCloseIntent(ctx, tenantID, request.Identity.ExecutionId)
	if err != nil || stored.OperationID != request.Identity.OperationId || stored.SpecHash != request.Identity.ExecutionSpecHash || stored.SourceGeneration != 41 || stored.Generation != 1 {
		t.Fatalf("ADMISSION_MATERIAL_BEHAVIOR: command ACK lacks the independently visible original close: %v", err)
	}
	response, err := modeldevv1.NewModelDevAdmissionServiceClient(connection).PrepareAdmission(rpcContext, &modeldevv1.PrepareAdmissionRequest{})
	if response != nil || status.Code(err) != codes.Unimplemented || status.Convert(err).Message() != "unknown service ani.modeldev.v1.ModelDevAdmissionService" {
		t.Fatalf("ADMISSION_MATERIAL_BEHAVIOR: omitted admission was registered or request failed outside the live command listener (code=%s)", status.Code(err))
	}
	assertProductionNotReadyWithoutBusinessAdapters(t, config.Server.Admin.Addr)
}

func TestAdmissionMaterialsFailStartupWithoutFallbackOrPoolLeak(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*testing.T, *conf.AdmissionResolution)
		message string
	}{
		{
			name: "missing facts file",
			change: func(t *testing.T, config *conf.AdmissionResolution) {
				config.FactsFiles[0].Path = filepath.Join(t.TempDir(), "private-missing-facts.json")
			},
			message: "managed command listener configuration invalid",
		},
		{
			name: "wrong facts byte pin",
			change: func(_ *testing.T, config *conf.AdmissionResolution) {
				pin := config.FactsFiles[0].Sha256
				first := "0"
				if pin[0] == '0' {
					first = "1"
				}
				config.FactsFiles[0].Sha256 = first + pin[1:]
			},
			message: "managed command listener configuration invalid",
		},
		{
			name: "unavailable catalogue directory",
			change: func(t *testing.T, config *conf.AdmissionResolution) {
				config.CatalogueDirectory = filepath.Join(t.TempDir(), "private-missing-catalogue")
			},
			message: "managed command listener configuration invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareConfiguredAdmission(t)
			observer := fixture.openPool()
			// A regression must not leave even this isolated test's runtime
			// connection behind. Never terminate another application or role.
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if _, err := observer.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1 AND usename = current_user AND datname = current_database()", fixture.applicationName); err != nil {
					t.Error("ADMISSION_MATERIAL_CLEANUP: exact task-owned connection cleanup failed")
				}
			})
			connection, stop := startConfiguredAdmissionApp(t, fixture)
			if countConfiguredAdmissionConnections(t, observer, fixture.applicationName) == 0 {
				t.Fatal("ADMISSION_MATERIAL_PREFLIGHT: actual runtime pool not observable; cleanup behavior NOT_RUN")
			}
			_ = connection.Close()
			stop()
			requireAdmissionMaterialPoolReleased(t, observer, fixture.applicationName)
			test.change(t, fixture.config.Command.AdmissionResolution)
			if err := fixture.config.Validate(); err != nil {
				t.Fatal("ADMISSION_MATERIAL_PREFLIGHT: bad material must retain valid reference shape; loading behavior NOT_RUN")
			}
			t.Log("ADMISSION_MATERIAL_PREFLIGHT PASS: real configured pool was independently observed and released before valid-shape material failure")
			app, err := buildApp(fixture.config, newRuntimeLogger(io.Discard))
			if app != nil {
				t.Cleanup(func() { _ = app.release() })
			}
			if app != nil || err == nil {
				t.Fatal("ADMISSION_MATERIAL_BEHAVIOR: explicit bad admission materials produced a usable fallback application")
			}
			if err.Error() != test.message {
				t.Fatal("ADMISSION_MATERIAL_BEHAVIOR: startup failure did not preserve its finite safe message")
			}
			requireAdmissionMaterialPoolReleased(t, observer, fixture.applicationName)
		})
	}
}

func TestAdmissionMaterialsEmptyDirectoryStartsButCannotPrepareImplementation(t *testing.T) {
	fixture := prepareConfiguredAdmission(t)
	// Facts and READY input remain real and pinned. Only the explicitly
	// configured directory is empty; startup must not invent software facts.
	fixture.config.Command.AdmissionResolution.CatalogueDirectory = t.TempDir()
	connection, _ := startConfiguredAdmissionApp(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requestID := uuid.NewString()
	rpcContext := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-ani-tenant-id", fixture.tenantID, "x-ani-actor", "governance:user:42", "x-ani-request-id", requestID,
	))
	response, err := modeldevv1.NewModelDevAdmissionServiceClient(connection).PrepareAdmission(rpcContext, fixture.request)
	if response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("ADMISSION_MATERIAL_BEHAVIOR: missing software returned a candidate or wrong rejection (code=%s)", status.Code(err))
	}
	failure := status.Convert(err)
	details := failure.Details()
	if len(details) != 1 {
		t.Fatal("ADMISSION_MATERIAL_BEHAVIOR: missing software did not return one typed safe detail")
	}
	detail, ok := details[0].(*modeldevv1.ErrorDetail)
	const safeMessage = "managed environment is not ready"
	if !ok || detail.Reason != modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY || detail.CorrelationId != requestID || detail.SafeMessage != safeMessage || failure.Message() != safeMessage || len(detail.Violations) != 0 {
		t.Fatal("ADMISSION_MATERIAL_BEHAVIOR: missing software changed its finite reason, correlation or safe message")
	}
	observer := fixture.openPool()
	stored, err := input.New(observer).Get(ctx, fixture.tenantID, fixture.ready.Import.InputVersionID)
	if err != nil || !reflect.DeepEqual(stored, fixture.ready) {
		t.Fatalf("ADMISSION_MATERIAL_BEHAVIOR: failed resolution changed the original READY input: %v", err)
	}
	var executions, identities, closes, dispatches int
	err = observer.QueryRow(ctx, `SELECT (SELECT count(*) FROM modeldev_executions),
		(SELECT count(*) FROM modeldev_execution_identities), (SELECT count(*) FROM modeldev_close_intents),
		(SELECT count(*) FROM modeldev_pipeline_dispatches)`).Scan(&executions, &identities, &closes, &dispatches)
	if err != nil || executions != 0 || identities != 0 || closes != 0 || dispatches != 0 {
		t.Fatalf("ADMISSION_MATERIAL_BEHAVIOR: rejected candidate persisted command facts: executions=%d identities=%d closes=%d dispatches=%d error=%v", executions, identities, closes, dispatches, err)
	}
	waitForHTTP(t, "http://"+fixture.config.Server.Admin.Addr+"/readyz")
}

func requireAdmissionMaterialPoolReleased(t *testing.T, observer *pgxpool.Pool, applicationName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for countConfiguredAdmissionConnections(t, observer, applicationName) != 0 {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("ADMISSION_MATERIAL_BEHAVIOR: application retained its exact runtime database connection")
		}
	}
}
