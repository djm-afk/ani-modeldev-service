package runtimeproof_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestComputeCleanupWaitsForConfirmedAsyncPodRemoval(t *testing.T) {
	for _, mode := range []string{"original eventually absent", "replacement UID", "unknown finalizer", "caller deadline"} {
		t.Run(mode, func(t *testing.T) {
			f, _, operations := exhaustedCleanup(t)
			path := ""
			for key, object := range f.objects {
				if object.GetKind() == "Pod" {
					path = key
				}
			}
			if path == "" {
				t.Fatal("original training Pod missing")
			}
			original := f.step.serveExtra
			var pending *unstructured.Unstructured
			reads, deletes := 0, 0
			// Only Kubernetes' asynchronous DELETE/GET boundary is substituted.
			// The original handler still validates the real committed PG receipt,
			// exact UID/RV and deletion policy before acknowledging the DELETE.
			f.step.serveExtra = func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != path {
					return original(t, w, r)
				}
				if r.Method == http.MethodDelete {
					deletes++
					pending = f.objects[path].DeepCopy()
					reply := httptest.NewRecorder()
					if !original(t, reply, r) || reply.Code != http.StatusOK {
						t.Fatal("original exact-UID DELETE was not accepted")
					}
					at := metav1.Now()
					pending.SetDeletionTimestamp(&at)
					pending.SetResourceVersion("async-deleting-4")
					if mode == "replacement UID" {
						pending.SetUID("same-name-replacement")
					}
					if mode == "unknown finalizer" {
						pending.SetFinalizers([]string{"unknown.example/hold"})
					}
					for key, values := range reply.Header() {
						w.Header()[key] = values
					}
					w.WriteHeader(reply.Code)
					_, _ = w.Write(reply.Body.Bytes())
					return true
				}
				if r.Method == http.MethodGet && pending != nil {
					reads++
					if mode == "original eventually absent" && reads == 2 {
						pending = nil
						return original(t, w, r) // The exact original now returns 404.
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(pending)
					return true
				}
				return original(t, w, r)
			}
			ctx := context.Background()
			if mode == "caller deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
			}
			_, err := operations.ReconcileWithActor(ctx, f.step.f.execution.TenantID, f.step.f.execution.ExecutionID, "governance:operator-3")
			state, readErr := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if readErr != nil || state.ComputeCleanup == nil || state.ComputeCleanup.Attempts != 4 || state.ComputeCleanup.OperatorRetry == nil || deletes != 1 || reads == 0 {
				t.Fatal("original one-shot cleanup was not exercised", readErr, state.ComputeCleanup, deletes, reads)
			}
			if mode == "original eventually absent" {
				if err != nil || state.ComputeCleanup.Phase != "APPLIED" || len(f.deleted) != 7 || reads != 2 {
					t.Fatalf("ASYNC_ORIGINAL_POD_REMOVAL_NOT_WAITED: phase=%s deletes=%v reads=%d err=%v", state.ComputeCleanup.Phase, f.deleted, reads, err)
				}
			} else if err == nil || state.ComputeCleanup.Phase == "APPLIED" || len(f.deleted) != 4 {
				t.Fatal("unsafe asynchronous removal was confirmed or DELETE was resent", mode, state.ComputeCleanup, f.deleted, err)
			}
			if mode == "caller deadline" && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("cleanup returned uncertainty before observing the caller deadline", ctx.Err(), err)
			}
		})
	}
}
