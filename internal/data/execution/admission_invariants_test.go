package execution_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
)

const (
	otherTenant    = "22222222-3333-4444-8555-666666666666"
	otherOperation = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa"
	otherExecution = "bbbbbbbb-cccc-4ddd-8eee-aaaaaaaaaaaa"
)

func TestAcceptConflictingIdentityOrFactsPreservesOriginalAdmission(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*biz.Admission)
	}{
		{"same operation different execution", func(a *biz.Admission) { a.ExecutionID = otherExecution }},
		{"same execution different operation", func(a *biz.Admission) { a.OperationID = otherOperation }},
		{"different actor", func(a *biz.Admission) { a.Actor = "governance:user:43" }},
		{"different acceptance time", func(a *biz.Admission) { a.AcceptedAt = a.AcceptedAt.Add(time.Microsecond) }},
		{"different intent", func(a *biz.Admission) { a.Intent.Name = "another-training" }},
		{"different intent presence with same resolved spec", func(a *biz.Admission) {
			seed := uint32(42)
			a.Intent.MLP = &cpup01.MLPParameters{Seed: &seed}
		}},
		{"different release contents", func(a *biz.Admission) { a.Snapshot.Implementation.PipelineIRSHA256 = strings.Repeat("9", 64) }},
		{"different deadline", func(a *biz.Admission) { a.Snapshot.DeadlineAt = a.Snapshot.DeadlineAt.Add(time.Minute) }},
		{"other tenant reuses both identities", func(a *biz.Admission) { a.TenantID = otherTenant }},
		{"other tenant reuses operation", func(a *biz.Admission) {
			a.TenantID, a.ExecutionID = otherTenant, otherExecution
		}},
		{"other tenant reuses execution", func(a *biz.Admission) {
			a.TenantID, a.OperationID = otherTenant, otherOperation
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			openRuntimePool := preparePostgreSQL(t)
			repository := execution.New(openRuntimePool())
			original := validAdmission(t)
			candidate := validAdmission(t)
			testCase.mutate(&candidate)
			refreshAdmissionHashes(t, &candidate)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := repository.Accept(ctx, original); err != nil {
				t.Fatalf("initial Accept: %v", err)
			}
			got, err := repository.Accept(ctx, candidate)
			assertEmptyFailure(t, got.Execution, err, biz.ErrAdmissionConflict)
			stored, err := repository.Get(ctx, original.TenantID, original.ExecutionID)
			if err != nil {
				t.Fatalf("Get original after conflict: %v", err)
			}
			assertOriginalAdmission(t, stored, original)
			if candidate.TenantID != original.TenantID || candidate.ExecutionID != original.ExecutionID {
				unexpected, err := repository.Get(ctx, candidate.TenantID, candidate.ExecutionID)
				assertEmptyFailure(t, unexpected, err, biz.ErrExecutionNotFound)
			}
		})
	}
}

func TestAcceptConcurrentDuplicatesAcrossSixPoolsReturnOneAdmission(t *testing.T) {
	openRuntimePool := preparePostgreSQL(t)
	command := validAdmission(t)
	command.Actor = "governance:user:42"
	commands := make([]biz.Admission, 6)
	for i := range commands {
		commands[i] = command
	}
	for _, outcome := range raceAdmissions(t, openRuntimePool, commands) {
		if outcome.err != nil {
			t.Errorf("concurrent duplicate %d rejected: %v", outcome.index, outcome.err)
			continue
		}
		assertOriginalAdmission(t, outcome.execution, command)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := execution.New(openRuntimePool()).Get(ctx, command.TenantID, command.ExecutionID)
	if err != nil {
		t.Fatalf("Get committed concurrent admission: %v", err)
	}
	assertOriginalAdmission(t, got, command)
}

func TestAcceptConcurrentDifferentFactsHaveOneWinner(t *testing.T) {
	openRuntimePool := preparePostgreSQL(t)
	first, second := validAdmission(t), validAdmission(t)
	second.Intent.Name = "competing-training"
	refreshAdmissionHashes(t, &second)
	commands := []biz.Admission{first, second}
	winner, successes := -1, 0
	for _, outcome := range raceAdmissions(t, openRuntimePool, commands) {
		if outcome.err == nil {
			winner = outcome.index
			successes++
			assertOriginalAdmission(t, outcome.execution, commands[outcome.index])
		} else {
			assertEmptyFailure(t, outcome.execution, outcome.err, biz.ErrAdmissionConflict)
		}
	}
	if successes != 1 {
		t.Fatalf("competing immutable commands produced %d winners, want exactly one", successes)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := execution.New(openRuntimePool()).Get(ctx, first.TenantID, first.ExecutionID)
	if err != nil {
		t.Fatalf("Get competing winner: %v", err)
	}
	assertOriginalAdmission(t, got, commands[winner])
}

func TestGetHidesOtherTenantExecutionAndReturnsOwnAdmission(t *testing.T) {
	openRuntimePool := preparePostgreSQL(t)
	repository := execution.New(openRuntimePool())
	first, second := validAdmission(t), validAdmission(t)
	second.TenantID, second.OperationID, second.ExecutionID = otherTenant, otherOperation, otherExecution
	second.Actor = "governance:access-key:17"
	refreshAdmissionHashes(t, &second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, command := range []biz.Admission{first, second} {
		if _, err := repository.Accept(ctx, command); err != nil {
			t.Fatalf("Accept independent tenant command: %v", err)
		}
		got, err := repository.Get(ctx, command.TenantID, command.ExecutionID)
		if err != nil {
			t.Fatalf("Get own tenant admission: %v", err)
		}
		assertOriginalAdmission(t, got, command)
	}
	for _, lookup := range [][2]string{
		{first.TenantID, second.ExecutionID},
		{second.TenantID, first.ExecutionID},
		{first.TenantID, "dddddddd-eeee-4fff-8aaa-bbbbbbbbbbbb"},
	} {
		got, err := repository.Get(ctx, lookup[0], lookup[1])
		assertEmptyFailure(t, got, err, biz.ErrExecutionNotFound)
	}
}

func TestGetRejectsMalformedUUIDSeparatorsWithoutReturningAdmission(t *testing.T) {
	openRuntimePool := preparePostgreSQL(t)
	repository := execution.New(openRuntimePool())
	command := validAdmission(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := repository.Accept(ctx, command); err != nil {
		t.Fatalf("Accept before malformed identity lookup: %v", err)
	}
	cases := []struct {
		name      string
		tenant    string
		execution string
	}{
		{"tenant separators", strings.ReplaceAll(command.TenantID, "-", "X"), command.ExecutionID},
		{"execution separators", command.TenantID, strings.ReplaceAll(command.ExecutionID, "-", "X")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := repository.Get(ctx, testCase.tenant, testCase.execution)
			assertEmptyFailure(t, got, err, biz.ErrInvalidAdmission)
		})
	}
	got, err := repository.Get(ctx, command.TenantID, command.ExecutionID)
	if err != nil {
		t.Fatalf("Get with the original valid identities: %v", err)
	}
	assertOriginalAdmission(t, got, command)
}

func TestAcceptInvalidAdmissionDoesNotReserveInbox(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*biz.Admission)
		rehash bool
	}{
		{"invalid tenant", func(a *biz.Admission) { a.TenantID = "not-a-uuid" }, false},
		{"zero operation", func(a *biz.Admission) { a.OperationID = "00000000-0000-0000-0000-000000000000" }, false},
		{"invalid execution", func(a *biz.Admission) { a.ExecutionID = "not-a-uuid" }, false},
		{"empty actor", func(a *biz.Admission) { a.Actor = "" }, false},
		{"actor whitespace", func(a *biz.Admission) { a.Actor = " governance:user:42" }, false},
		{"actor control character", func(a *biz.Admission) { a.Actor = "governance:user:42\n" }, false},
		{"intent hash mismatch", func(a *biz.Admission) { a.IntentHash = strings.Repeat("0", 64) }, false},
		{"snapshot hash mismatch", func(a *biz.Admission) { a.SpecHash = strings.Repeat("0", 64) }, false},
		{"intent snapshot preset mismatch", func(a *biz.Admission) { a.Intent.ComputePresetID = otherExecution }, true},
		{"intent snapshot input mismatch", func(a *biz.Admission) { a.Intent.DatasetVersionID = otherExecution }, true},
		{"intent snapshot image mismatch", func(a *biz.Admission) {
			a.Intent = conformance.FinetuningIntentV1()
		}, true},
		{"explicit resolved parameter mismatch", func(a *biz.Admission) {
			rate := "0.02"
			a.Intent.MLP = &cpup01.MLPParameters{LearningRate: &rate}
		}, true},
		{"zero acceptance time", func(a *biz.Admission) { a.AcceptedAt = time.Time{} }, false},
		{"submicrosecond acceptance time", func(a *biz.Admission) { a.AcceptedAt = a.AcceptedAt.Add(time.Nanosecond) }, false},
		{"acceptance at deadline", func(a *biz.Admission) { a.AcceptedAt = a.Snapshot.DeadlineAt }, false},
		{"acceptance after deadline", func(a *biz.Admission) { a.AcceptedAt = a.Snapshot.DeadlineAt.Add(time.Second) }, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			openRuntimePool := preparePostgreSQL(t)
			repository := execution.New(openRuntimePool())
			original, invalid := validAdmission(t), validAdmission(t)
			testCase.mutate(&invalid)
			if testCase.rehash {
				refreshAdmissionHashes(t, &invalid)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			receipt, err := repository.Accept(ctx, invalid)
			assertEmptyFailure(t, receipt.Execution, err, biz.ErrInvalidAdmission)
			got, err := repository.Get(ctx, original.TenantID, original.ExecutionID)
			assertEmptyFailure(t, got, err, biz.ErrExecutionNotFound)
			// Repairing the same command must still admit it. A rejected input
			// cannot reserve its operation or execution identity in an inbox.
			receipt, err = repository.Accept(ctx, original)
			if err != nil {
				t.Fatalf("corrected admission was poisoned by invalid delivery: %v", err)
			}
			assertOriginalAdmission(t, receipt.Execution, original)
		})
	}
}

func refreshAdmissionHashes(t *testing.T, admission *biz.Admission) {
	t.Helper()
	*admission = admissionfixture.Bind(t, *admission)
}

func assertEmptyFailure(t *testing.T, got biz.Execution, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Errorf("want stable %s without storage/identity details, got %v", want, err)
	}
	if !reflect.DeepEqual(got, biz.Execution{}) {
		t.Error("failed request exposed an execution record")
	}
}

type admissionOutcome struct {
	index     int
	execution biz.Execution
	err       error
}

func raceAdmissions(t *testing.T, openRuntimePool func() *pgxpool.Pool, commands []biz.Admission) []admissionOutcome {
	t.Helper()
	repositories := make([]*execution.Repository, len(commands))
	for i := range repositories {
		repositories[i] = execution.New(openRuntimePool())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan admissionOutcome, len(commands))
	for i, command := range commands {
		go func(index int, admission biz.Admission) {
			<-start
			got, err := repositories[index].Accept(ctx, admission)
			results <- admissionOutcome{index: index, execution: got.Execution, err: err}
		}(i, command)
	}
	close(start)
	outcomes := make([]admissionOutcome, 0, len(commands))
	for range commands {
		select {
		case outcome := <-results:
			outcomes = append(outcomes, outcome)
		case <-ctx.Done():
			t.Fatal("bounded concurrent admission test timed out")
		}
	}
	return outcomes
}
