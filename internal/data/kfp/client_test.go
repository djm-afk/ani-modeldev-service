package kfp_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
)

func TestCreateRunResponseLostPreservesUncertaintyWithoutResending(t *testing.T) {
	// This TLS server substitutes only the KFP HTTP boundary. It is not evidence
	// of a real Run, durable SUBMITTING, tenant authorization, or ENV readiness.
	admission := fixtureAdmission(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/apis/v2beta1/runs" || r.URL.RawQuery != "" {
			t.Errorf("unexpected KFP operation: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-fixture-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("request lacks regenerated fixture identity or JSON content type")
		}
		for _, key := range []string{"Idempotency-Key", "X-Idempotency-Key", "Cookie", "Kubeflow-Userid"} {
			if len(r.Header.Values(key)) != 0 {
				t.Errorf("unexpected forwarded or retry-enabling header %s", key)
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read fixture request: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		// Handwritten against KFP 2.16.0 run.proto / runtime_config.proto.
		// No source implementation or encoder is used to form the expectation.
		var want map[string]any
		if err := json.Unmarshal([]byte(`{
  "experiment_id":"44444444-4444-4444-8444-444444444444",
  "display_name":"md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
  "pipeline_version_reference":{
    "pipeline_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
    "pipeline_version_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"
  },
  "runtime_config":{
    "parameters":{
      "execution_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
      "spec_hash":"972dee14e65202d5d4da7da199cf5f37139b535701a0cb1b3de4d8ef8a5170b9"
    },
    "pipeline_root":"s3://fixture-kfp-artifacts/managed-root"
  },
  "service_account":"cpu-managed-step"
}`), &want); err != nil {
			t.Errorf("invalid handwritten fixture: %v", err)
		}
		want["runtime_config"].(map[string]any)["parameters"].(map[string]any)["spec_hash"] = admission.SpecHash
		if !reflect.DeepEqual(got, want) {
			t.Error("request does not match the fixed owner-controlled CreateRun contract")
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("disconnect response fixture: %v", err)
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(server.Close)
	var tokenCalls atomic.Int32
	provider := tokenProviderFunc(func(_ context.Context, tenant string, binding cpup01.EnvironmentBindingSnapshot) (string, error) {
		tokenCalls.Add(1)
		if tenant != admission.TenantID || binding != admission.Snapshot.Environment {
			t.Error("identity provider did not receive the frozen trusted tenant binding")
		}
		return "synthetic-fixture-token", nil
	})
	client := fixtureClient(t, server, provider)
	observation, err := client.CreateRun(context.Background(), fixtureCreateRequest(t, admission))
	if err == nil || observation.State != biz.PipelineSubmissionUncertain || observation.RunID != "" {
		t.Errorf("lost creation response must remain uncertain with no confirmed Run: %+v, %v", observation, err)
	}
	if calls.Load() != 1 || tokenCalls.Load() != 1 {
		t.Errorf("want exactly one credential generation and POST; got %d credentials and %d requests", tokenCalls.Load(), calls.Load())
	}
}

type tokenProviderFunc func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error)

func (provider tokenProviderFunc) BearerToken(ctx context.Context, tenant string, binding cpup01.EnvironmentBindingSnapshot) (string, error) {
	return provider(ctx, tenant, binding)
}

func fixtureAdmission(t *testing.T) biz.Admission {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	intent := cpup01.Intent{Name: "kfp-boundary-fixture", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatalf("fixture intent: %v", err)
	}
	return admissionfixture.Bind(t, biz.Admission{
		TenantID:    "11111111-2222-4333-8444-555555555555",
		Actor:       "governance:synthetic-fixture",
		OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff",
		ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Intent:      intent, IntentHash: intentHash,
		Snapshot: snapshot, SpecHash: conformance.SnapshotSHA256V1,
		AcceptedAt: snapshot.DeadlineAt.Add(-time.Hour).UTC(),
	})
}

// This synthetic permit exercises only the outbound seam. It is not evidence
// of a committed reservation; the submitter integration test uses real PG.
func fixtureCreateRequest(t *testing.T, admission biz.Admission) biz.PipelineCreateRequest {
	t.Helper()
	plan, err := (biz.PipelineDispatchRequest{
		Admission: admission,
		Owner:     biz.PipelineOwnerConfiguration{Reference: "cpu07-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"},
	}).Freeze()
	if err != nil {
		t.Fatalf("freeze create request fixture: %v", err)
	}
	hash, err := plan.Digest()
	if err != nil {
		t.Fatalf("hash create request fixture: %v", err)
	}
	return biz.PipelineCreateRequest{
		Admission: admission, Plan: plan,
		Permit: biz.PipelineSendPermit{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, AttemptID: "66666666-7777-4888-8999-aaaaaaaaaaaa", PlanHash: hash},
	}
}

func TestCreateRunConfirmsOnlyCompleteMatchingOfficialResponse(t *testing.T) {
	// KFP 2.16.0 api_converter.go:1430-1503 returns these associations after
	// CreateRun, but may return HTTP 200 with only run_id/experiment_id/error
	// when conversion fails. Matching a response is not Pod/namespace identity
	// proof, caller authorization, or an authoritative Run binding.
	response := `{
  "run_id":"55555555-6666-4777-8888-999999999999",
  "experiment_id":"44444444-4444-4444-8444-444444444444",
  "display_name":"md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
  "pipeline_version_reference":{
    "pipeline_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
    "pipeline_version_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"
  },
  "runtime_config":{
    "parameters":{
      "execution_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
      "spec_hash":"972dee14e65202d5d4da7da199cf5f37139b535701a0cb1b3de4d8ef8a5170b9"
    },
    "pipeline_root":"s3://fixture-kfp-artifacts/managed-root"
  },
  "service_account":"cpu-managed-step",
  "state":"PENDING",
  "created_at":"2026-09-30T09:00:00Z"
}`
	fixture := fixtureAdmission(t)
	response = strings.ReplaceAll(response, "972dee14e65202d5d4da7da199cf5f37139b535701a0cb1b3de4d8ef8a5170b9", fixture.SpecHash)
	cases := []struct {
		name          string
		body          string
		status        int
		contentType   string
		redirect      bool
		confirmed     bool
		expectedRunID string
	}{
		{name: "complete official response", body: response, confirmed: true},
		{name: "created run may already report failed computation", body: strings.Replace(response, `"state":"PENDING"`, `"state":"FAILED"`, 1), confirmed: true},
		{name: "uppercase Run UUID is returned lowercase", body: strings.Replace(response, "55555555-6666-4777-8888-999999999999", "ABCDEFAB-CDEF-4ABC-8DEF-ABCDEFABCDEF", 1), confirmed: true, expectedRunID: "abcdefab-cdef-4abc-8def-abcdefabcdef"},
		{name: "JSON with charset", body: response, contentType: "application/json; charset=UTF-8", confirmed: true},
		{name: "additional output structures", body: strings.TrimSuffix(response, "}") + `,"extra":[1,{"ok":true},null]}`, confirmed: true},
		{name: "null response", body: `null`},
		{name: "null service account", body: strings.Replace(response, `"service_account":"cpu-managed-step"`, `"service_account":null`, 1)},
		{name: "null error is not confirmation", body: strings.TrimSuffix(response, "}") + `,"error":null}`},
		{name: "zero Run UUID", body: strings.Replace(response, "55555555-6666-4777-8888-999999999999", "00000000-0000-0000-0000-000000000000", 1)},
		{name: "invalid UTF8 in output", body: strings.TrimSuffix(response, "}") + ",\"extra\":\"\xff\"}"},
		{name: "excessive JSON depth", body: strings.TrimSuffix(response, "}") + `,"extra":` + strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34) + "}"},
		{name: "conversion error with run ID", body: `{"run_id":"55555555-6666-4777-8888-999999999999","experiment_id":"44444444-4444-4444-8444-444444444444","error":{"code":13,"message":"synthetic-sensitive-error"}}`},
		{name: "error in otherwise complete response", body: strings.TrimSuffix(response, "}") + `,"error":{"code":13,"message":"synthetic-sensitive-error"}}`},
		{name: "missing run ID", body: strings.Replace(response, `"run_id":"55555555-6666-4777-8888-999999999999",`, "", 1)},
		{name: "invalid run ID", body: strings.Replace(response, "55555555-6666-4777-8888-999999999999", "NOT_RUN", 1)},
		{name: "other experiment", body: strings.Replace(response, "44444444-4444-4444-8444-444444444444", "44444444-4444-4444-8444-555555555555", 1)},
		{name: "other pipeline", body: strings.Replace(response, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "cccccccc-cccc-4ccc-8ccc-dddddddddddd", 1)},
		{name: "other pipeline version", body: strings.Replace(response, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "dddddddd-dddd-4ddd-8ddd-cccccccccccc", 1)},
		{name: "other execution", body: strings.ReplaceAll(response, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "aaaaaaaa-bbbb-4ccc-8ddd-ffffffffffff")},
		{name: "other spec", body: strings.Replace(response, fixture.SpecHash, strings.Repeat("f", 64), 1)},
		{name: "other step service account", body: strings.Replace(response, "cpu-managed-step", "cpu-training", 1)},
		{name: "other artifact root", body: strings.Replace(response, "managed-root", "other-root", 1)},
		{name: "duplicate run ID", body: strings.TrimSuffix(response, "}") + `,"run_id":"55555555-6666-4777-8888-999999999999"}`},
		{name: "duplicate nested spec", body: strings.Replace(response, `"spec_hash":`, `"spec_hash":"different","spec_hash":`, 1)},
		{name: "case alias is not an official key", body: strings.Replace(response, `"run_id":`, `"RUN_ID":`, 1)},
		{name: "truncated JSON", body: strings.TrimSuffix(response, "}")},
		{name: "trailing JSON", body: response + `{}`},
		{name: "exceeds one MiB response limit", body: strings.Repeat(" ", 1<<20) + response},
		{name: "HTML media type", body: response, contentType: "text/html"},
		{name: "undocumented success status", body: response, status: http.StatusCreated},
		{name: "redirect", body: response, status: http.StatusTemporaryRedirect, redirect: true},
		{name: "bad request", body: response, status: http.StatusBadRequest},
		{name: "forbidden", body: response, status: http.StatusForbidden},
		{name: "not found", body: response, status: http.StatusNotFound},
		{name: "conflict", body: response, status: http.StatusConflict},
		{name: "rate limited", body: response, status: http.StatusTooManyRequests},
		{name: "internal error", body: response, status: http.StatusInternalServerError},
		{name: "unavailable", body: response, status: http.StatusServiceUnavailable},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				mediaType := test.contentType
				if mediaType == "" {
					mediaType = "application/json"
				}
				w.Header().Set("Content-Type", mediaType)
				status := test.status
				if status == 0 {
					status = http.StatusOK
				}
				if test.redirect && r.URL.Path == "/apis/v2beta1/runs" {
					w.Header().Set("Location", server.URL+"/redirected")
				} else if test.redirect {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(server.Close)
			client := fixtureClient(t, server, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
				return "synthetic-fixture-token", nil
			}))
			observation, err := client.CreateRun(context.Background(), fixtureCreateRequest(t, fixture))
			if test.confirmed {
				expectedRunID := test.expectedRunID
				if expectedRunID == "" {
					expectedRunID = "55555555-6666-4777-8888-999999999999"
				}
				if err != nil || observation.State != biz.PipelineSubmissionConfirmed || observation.RunID != expectedRunID {
					t.Errorf("matching official response was not confirmed: %+v, %v", observation, err)
				}
			} else if err == nil || observation.State != biz.PipelineSubmissionUncertain || observation.RunID != "" {
				t.Errorf("untrusted response must remain uncertain: %+v, %v", observation, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-sensitive-error") {
				t.Error("raw KFP response leaked through returned error")
			}
			if calls.Load() != 1 {
				t.Errorf("CreateRun repeated or redirected POST: calls=%d", calls.Load())
			}
		})
	}
}

func fixtureClient(t *testing.T, server *httptest.Server, provider kfp.TokenProvider) *kfp.Client {
	t.Helper()
	certificates := x509.NewCertPool()
	certificates.AddCert(server.Certificate())
	client, err := kfp.New(kfp.Config{
		ConnectionRef: "kfp-managed-v1", Endpoint: server.URL,
		RootCAs: certificates, Timeout: 3 * time.Second,
	}, provider)
	if err != nil {
		t.Fatalf("construct fixture client: %v", err)
	}
	return client
}
