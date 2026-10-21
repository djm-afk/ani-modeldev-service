package resolutiontest

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"reflect"
	"testing"
	"time"
)

func TestPrepareRejectsIncompatibleManagedFactsAndInvalidTiming(t *testing.T) {
	f := prepareResolution(t)
	for _, test := range []struct {
		name   string
		change func(*biz.TenantAdmissionFacts)
	}{
		{"workspace", func(v *biz.TenantAdmissionFacts) { v.Workspace.StorageClass = "other" }},
		{"input connection", func(v *biz.TenantAdmissionFacts) { v.InputScope.StorageConnectionID = "other" }},
		{"input bucket", func(v *biz.TenantAdmissionFacts) { v.InputScope.Bucket = "other" }},
		{"prefix boundary", func(v *biz.TenantAdmissionFacts) { v.InputScope.ApprovedPrefix = "fixed/in" }},
		{"relative path", func(v *biz.TenantAdmissionFacts) { v.InputFilePath = "relative.csv" }},
		{"path traversal", func(v *biz.TenantAdmissionFacts) { v.InputFilePath = "/workspace/../data.csv" }},
		{"same output", func(v *biz.TenantAdmissionFacts) { v.OutputDirectoryPath = v.InputFilePath }},
		{"nested output", func(v *biz.TenantAdmissionFacts) { v.OutputDirectoryPath = v.InputFilePath + "/out" }},
		{"unknown cluster", func(v *biz.TenantAdmissionFacts) { v.Environment.ClusterID = "UNKNOWN" }},
		{"namespace uid", func(v *biz.TenantAdmissionFacts) { v.Environment.NamespaceUID = "" }},
		{"shared service account", func(v *biz.TenantAdmissionFacts) {
			v.Environment.Identities.TrainerServiceAccount = v.Environment.Identities.KFPStepServiceAccount
		}},
		{"publication authority", func(v *biz.TenantAdmissionFacts) { v.PublicationScope.CredentialReference = "" }},
		{"missing timing", func(v *biz.TenantAdmissionFacts) { v.Timing = cpup01.TimingConfiguration{} }},
		{"default above max", func(v *biz.TenantAdmissionFacts) {
			v.Timing.ExecutionTimeoutDefaultSeconds = v.Timing.ExecutionTimeoutMaxSeconds + 1
		}},
		{"S above max", func(v *biz.TenantAdmissionFacts) {
			v.Timing.StepTimeoutSeconds = v.Timing.ExecutionTimeoutMaxSeconds + 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			v := f.facts
			test.change(&v)
			f.factReader.override = &v
			candidate, e := f.resolver.Prepare(f.ctx, f.request)
			if e == nil || !reflect.DeepEqual(candidate, cpup01.PreparedCandidate{}) {
				t.Fatal("unsafe facts produced candidate", e)
			}
		})
	}
	f.factReader.override = nil
	requireNoResolvedExecution(t, f)
}
func TestPrepareFixedMicrosecondTimeAndContextCancellation(t *testing.T) {
	f := prepareResolution(t)
	for _, at := range []time.Time{{}, f.request.AcceptedAt.Add(time.Nanosecond), time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)} {
		r := f.request
		r.AcceptedAt = at
		if _, e := f.resolver.Prepare(f.ctx, r); e == nil {
			t.Fatal("unrepresentable admission time accepted")
		}
	}
	canceled, cancel := context.WithCancel(f.ctx)
	cancel()
	expired, finish := context.WithDeadline(f.ctx, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	defer finish()
	for _, ctx := range []context.Context{canceled, expired} {
		before := f.materials.reads
		got, e := f.resolver.Prepare(ctx, f.request)
		if (!errors.Is(e, context.Canceled) && !errors.Is(e, context.DeadlineExceeded)) || !reflect.DeepEqual(got, cpup01.PreparedCandidate{}) || f.materials.reads != before {
			t.Fatal("canceled compile read provider or returned candidate")
		}
	}
	requireNoResolvedExecution(t, f)
}
