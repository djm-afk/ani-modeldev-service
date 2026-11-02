package runtimeproof

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"testing"
)

func TestRootRuntimeConfigRequiresExactFourOriginalParameters(t *testing.T) {
	plan := biz.PipelineDispatchPlan{ExecutionID: "execution-original", SpecHash: "hash-original", Environment: cpup01.EnvironmentBindingSnapshot{BindingID: "environment-original", NamespaceUID: "namespace-original"}, Owner: biz.PipelineOwnerConfiguration{PipelineRoot: "s3://artifacts/artifacts/tenant/execution"}}
	valid := `{"parameterValues":{"execution_id":"execution-original","spec_hash":"hash-original","environment_binding_id":"environment-original","namespace_uid":"namespace-original"},"gcsOutputDirectory":"s3://artifacts/artifacts/tenant/execution"}`
	if !rootRuntimeConfigMatches(valid, plan) {
		t.Fatal("valid four-parameter runtime config rejected")
	}
	for _, raw := range []string{
		`{"parameterValues":{"execution_id":"execution-original","spec_hash":"hash-original"},"gcsOutputDirectory":"s3://artifacts/artifacts/tenant/execution"}`,
		`{"parameterValues":{"execution_id":"execution-original","spec_hash":"hash-original","environment_binding_id":"environment-original","namespace_uid":"wrong"},"gcsOutputDirectory":"s3://artifacts/artifacts/tenant/execution"}`,
		`{"parameterValues":{"execution_id":"execution-original","spec_hash":"hash-original","environment_binding_id":"environment-original","namespace_uid":"wrong","namespace_uid":"namespace-original"},"gcsOutputDirectory":"s3://artifacts/artifacts/tenant/execution"}`,
		valid + `{}`} {
		if rootRuntimeConfigMatches(raw, plan) {
			t.Fatal("unknown, duplicate or replaced root config accepted", raw)
		}
	}
}
