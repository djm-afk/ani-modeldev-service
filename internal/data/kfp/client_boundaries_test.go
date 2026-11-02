package kfp_test

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
)

func TestNewRequiresExplicitProtectedOwnerConfiguration(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("constructor sent a request") }))
	t.Cleanup(server.Close)
	certificates := x509.NewCertPool()
	certificates.AddCert(server.Certificate())
	provider := tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		t.Error("constructor requested credentials")
		return "", nil
	})
	cases := []struct {
		name   string
		mutate func(*kfp.Config)
	}{
		{"missing connection", func(c *kfp.Config) { c.ConnectionRef = "" }},
		{"no endpoint", func(c *kfp.Config) { c.Endpoint = "" }},
		{"plaintext HTTP", func(c *kfp.Config) { c.Endpoint = "http://fixture.example.test" }},
		{"credential-bearing endpoint", func(c *kfp.Config) { c.Endpoint = "https://user:synthetic@fixture.example.test" }},
		{"endpoint query", func(c *kfp.Config) { c.Endpoint = server.URL + "?token=synthetic" }},
		{"endpoint fragment", func(c *kfp.Config) { c.Endpoint = server.URL + "#fragment" }},
		{"endpoint traversal", func(c *kfp.Config) { c.Endpoint = server.URL + "/a/../b" }},
		{"no explicit CA", func(c *kfp.Config) { c.RootCAs = nil }},
		{"no timeout", func(c *kfp.Config) { c.Timeout = 0 }},
		{"unbounded timeout", func(c *kfp.Config) { c.Timeout = time.Hour }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config := kfp.Config{ConnectionRef: "kfp-managed-v1", Endpoint: server.URL, RootCAs: certificates, Timeout: time.Second}
			test.mutate(&config)
			client, err := kfp.New(config, provider)
			if client != nil || !errors.Is(err, kfp.ErrInvalidConfig) {
				t.Fatalf("unsafe owner configuration accepted: client=%v error=%v", client != nil, err)
			}
		})
	}
	t.Run("no provider", func(t *testing.T) {
		config := kfp.Config{ConnectionRef: "kfp-managed-v1", Endpoint: server.URL, RootCAs: certificates, Timeout: time.Second}
		if client, err := kfp.New(config, nil); client != nil || !errors.Is(err, kfp.ErrInvalidConfig) {
			t.Fatalf("missing provider accepted: client=%v error=%v", client != nil, err)
		}
	})
}

func TestCreateRunLocalFailuresNeverSendOrLeakCredentials(t *testing.T) {
	cases := []struct {
		name             string
		mutate           func(*biz.PipelineCreateRequest)
		token            string
		providerError    bool
		cancelBefore     bool
		cancelInProvider bool
		nilContext       bool
		wantTokenCalls   int32
	}{
		{name: "invalid hash", mutate: func(r *biz.PipelineCreateRequest) { r.Admission.SpecHash = strings.Repeat("f", 64) }},
		{name: "other connection", mutate: func(r *biz.PipelineCreateRequest) {
			r.Admission.Snapshot.Environment.KFPConnectionRef = "other-kfp-v1"
			r.Admission.SpecHash, _ = r.Admission.Snapshot.Digest()
			*r = fixtureCreateRequest(t, r.Admission)
		}},
		// Root is now frozen input, so the original constructor rejections
		// move here and still require zero credentials and zero HTTP calls.
		{name: "no root", mutate: func(r *biz.PipelineCreateRequest) { r.Plan.Owner.PipelineRoot = "" }},
		{name: "root traversal", mutate: func(r *biz.PipelineCreateRequest) { r.Plan.Owner.PipelineRoot = "s3://fixture-kfp-artifacts/a/../b" }},
		{name: "root temporary query", mutate: func(r *biz.PipelineCreateRequest) {
			r.Plan.Owner.PipelineRoot = "s3://fixture-kfp-artifacts/managed-root?signature=synthetic"
		}},
		{name: "plan differs from admission", mutate: func(r *biz.PipelineCreateRequest) { r.Plan.Environment.Identities.KFPStepServiceAccount = "other-step" }},
		{name: "other permit tenant", mutate: func(r *biz.PipelineCreateRequest) { r.Permit.TenantID = "22222222-2222-4222-8222-222222222222" }},
		{name: "other permit execution", mutate: func(r *biz.PipelineCreateRequest) { r.Permit.ExecutionID = "22222222-2222-4222-8222-222222222222" }},
		{name: "zero permit attempt", mutate: func(r *biz.PipelineCreateRequest) { r.Permit.AttemptID = "00000000-0000-0000-0000-000000000000" }},
		{name: "other permit hash", mutate: func(r *biz.PipelineCreateRequest) { r.Permit.PlanHash = strings.Repeat("f", 64) }},
		{name: "already canceled", cancelBefore: true},
		{name: "nil context", nilContext: true},
		{name: "provider denied", providerError: true, wantTokenCalls: 1},
		{name: "empty token", wantTokenCalls: 1},
		{name: "header injection token", token: "synthetic\r\nCookie: fixture", wantTokenCalls: 1},
		{name: "oversized token", token: strings.Repeat("a", 16385), wantTokenCalls: 1},
		{name: "canceled during identity generation", token: "synthetic-fixture-token", cancelInProvider: true, wantTokenCalls: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var requests, tokenCalls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
				tokenCalls.Add(1)
				if test.cancelInProvider {
					cancel()
				}
				if test.providerError {
					return "synthetic-sensitive-token", errors.New("synthetic-sensitive-provider-error")
				}
				return test.token, nil
			})
			client := fixtureClient(t, server, provider)
			request := fixtureCreateRequest(t, fixtureAdmission(t))
			if test.mutate != nil {
				test.mutate(&request)
			}
			if test.cancelBefore {
				cancel()
			}
			if test.nilContext {
				ctx = nil
			}
			observation, err := client.CreateRun(ctx, request)
			if !errors.Is(err, kfp.ErrNotSent) || observation.State != biz.PipelineSubmissionNotSent || observation.RunID != "" {
				t.Errorf("local failure did not remain NotSent: %+v, %v", observation, err)
			}
			if requests.Load() != 0 || tokenCalls.Load() != test.wantTokenCalls {
				t.Errorf("unexpected side effects: HTTP=%d credentials=%d", requests.Load(), tokenCalls.Load())
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-sensitive") {
				t.Error("provider detail leaked")
			}
		})
	}
}

func TestCreateRunCancellationAfterReceiptRemainsUncertain(t *testing.T) {
	received := make(chan struct{}, 4)
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case received <- struct{}{}:
		default:
		}
		<-release
	}))
	t.Cleanup(server.Close)
	defer close(release)
	client := fixtureClient(t, server, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-fixture-token", nil
	}))
	request := fixtureCreateRequest(t, fixtureAdmission(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		observation biz.PipelineSubmissionObservation
		err         error
	}
	done := make(chan result, 1)
	go func() { observation, err := client.CreateRun(ctx, request); done <- result{observation, err} }()
	select {
	case <-received:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("fixture never observed the request; sent cancellation not verified")
	}
	select {
	case got := <-done:
		if !errors.Is(got.err, kfp.ErrUncertain) || got.observation.State != biz.PipelineSubmissionUncertain || got.observation.RunID != "" {
			t.Fatalf("sent cancellation falsely resolved: %+v %v", got.observation, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not respect cancellation")
	}
	if requests.Load() != 1 {
		t.Errorf("cancellation retried POST: %d", requests.Load())
	}
}

func TestCreateRunRejectsUntrustedTLSCertificate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := kfp.New(kfp.Config{ConnectionRef: "kfp-managed-v1", Endpoint: server.URL, RootCAs: x509.NewCertPool(), Timeout: time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-fixture-token", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	client.WithMutationIdentity(fixtureMutationIdentity())
	observation, err := client.CreateRun(context.Background(), fixtureCreateRequest(t, fixtureAdmission(t)))
	if !errors.Is(err, kfp.ErrUncertain) || observation.State != biz.PipelineSubmissionUncertain || requests.Load() != 0 {
		t.Fatalf("TLS peer not rejected: %+v %v requests=%d", observation, err, requests.Load())
	}
}
