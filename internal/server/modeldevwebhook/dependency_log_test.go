package modeldevwebhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type diagnosticIdentities struct {
	*identities
	stage   string
	failure error
	wait    bool
}

func (r *diagnosticIdentities) fail(ctx context.Context) error {
	if r.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.failure
}
func (r *diagnosticIdentities) Namespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	if r.stage == "namespace" {
		return nil, r.fail(ctx)
	}
	return r.identities.Namespace(ctx, name)
}
func (r *diagnosticIdentities) Workflow(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	if r.stage == "workflow" {
		return nil, r.fail(ctx)
	}
	return r.identities.Workflow(ctx, namespace, name)
}

func TestDependencyAdmissionFailureLogsOnlyBoundedStageClassAndElapsed(t *testing.T) {
	const secret = "DO_NOT_LOG_SECRET_TOKEN_OR_API_BODY"
	for _, test := range []struct {
		name, stage, wantStage, wantClass string
		failure                           error
		parent                            string
	}{
		{"namespace deadline", "namespace", "namespace", "deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), ""},
		{"workflow canceled", "workflow", "workflow", "canceled", fmt.Errorf("%s: %w", secret, context.Canceled), ""},
		{"namespace forbidden", "namespace", "namespace", "forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, secret, errors.New(secret)), ""},
		{"workflow not found", "workflow", "workflow", "not_found", apierrors.NewNotFound(schema.GroupResource{Group: "argoproj.io", Resource: "workflows"}, secret), ""},
		{"workflow transport", "workflow", "workflow", "transport_or_unavailable", errors.New("https://user:" + secret + "@api.invalid/private?token=" + secret), ""},
		{"namespace unavailable nil", "namespace", "namespace", "transport_or_unavailable", nil, ""},
		{"workflow dependency deadline", "workflow", "workflow", "deadline", nil, "dependency_deadline"},
		{"parent canceled", "namespace", "request", "canceled", nil, "canceled"},
		{"parent deadline", "workflow", "request", "deadline", nil, "deadline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, base := fixtureConfig(t)
			if test.parent == "dependency_deadline" {
				config.DependencyTimeoutMilliseconds = 50
			}
			reader := &diagnosticIdentities{identities: base, stage: test.stage, failure: test.failure, wait: test.parent != ""}
			var logs bytes.Buffer
			handler, err := modeldevwebhook.New(config, reader, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			cancel := func() {}
			if test.parent == "canceled" {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if test.parent == "deadline" {
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
			}
			defer cancel()
			raw, err := json.Marshal(request(t, config, "sft-v1", "export"))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/mutate", bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			var result admissionv1.AdmissionReview
			if json.Unmarshal(rec.Body.Bytes(), &result) != nil || rec.Code != 200 || result.Response == nil || result.Response.Allowed || result.Response.Result == nil || result.Response.Result.Code != 503 || result.Response.Result.Reason != metav1.StatusReasonServiceUnavailable || !apierrors.IsServiceUnavailable(apierrors.FromObject(result.Response.Result)) || result.Response.Result.Message != "ModelDev Pod admission refused: dependency_unavailable" || len(result.Response.Patch) != 0 {
				t.Fatalf("logging changed admission refusal: %d %s", rec.Code, rec.Body.String())
			}
			var event map[string]any
			if json.Unmarshal(logs.Bytes(), &event) != nil || event["stage"] != test.wantStage || event["error_class"] != test.wantClass {
				t.Fatalf("bounded failure diagnostics missing: %s", logs.String())
			}
			elapsed, ok := event["elapsed_ms"].(float64)
			if !ok || elapsed < 0 {
				t.Fatalf("elapsed milliseconds missing: %s", logs.String())
			}
			for key := range event {
				switch key {
				case "time", "level", "msg", "decision", "allowed", "stage", "error_class", "elapsed_ms":
				default:
					t.Fatalf("unapproved diagnostic key %q", key)
				}
			}
			for _, private := range []string{secret, "api.invalid", namespaceUID, workflowUID, runID, requestUID, "test-tenant", "/workspace"} {
				if strings.Contains(logs.String(), private) {
					t.Fatalf("private error or identity logged: %s", logs.String())
				}
			}
		})
	}
}

func TestAdmissionSuccessAndPolicyDenialDoNotAcquireDependencyDiagnosticFields(t *testing.T) {
	for _, deny := range []bool{false, true} {
		config, reader := fixtureConfig(t)
		var logs bytes.Buffer
		handler, err := modeldevwebhook.New(config, reader, slog.New(slog.NewJSONHandler(&logs, nil)))
		if err != nil {
			t.Fatal(err)
		}
		body := request(t, config, "sft-v1", "export")
		if deny {
			body.Request.UserInfo.Username = "foreign-creator"
		}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/mutate", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var result admissionv1.AdmissionReview
		if json.Unmarshal(rec.Body.Bytes(), &result) != nil || result.Response == nil || result.Response.Allowed == deny {
			t.Fatal("existing admission behavior changed")
		}
		var event map[string]any
		if json.Unmarshal(logs.Bytes(), &event) != nil {
			t.Fatal("one existing decision log expected")
		}
		for _, key := range []string{"stage", "error_class", "elapsed_ms"} {
			if _, found := event[key]; found {
				t.Fatal("non-dependency decision acquired failure fields", event)
			}
		}
	}
}
