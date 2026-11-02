//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/storagecredentials"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func TestManagedStorageCredentialsRequireCurrentExecutionAuthorityAndRefuseCloseRefresh(t *testing.T) {
	f := newCompleteFixture(t)
	var issued atomic.Int32
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	sts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil || r.Form.Get("Action") != "AssumeRole" || !strings.Contains(r.Form.Get("Policy"), f.request.Admission.Snapshot.Input.Object.Key) || !strings.Contains(r.Header.Get("Authorization"), "/s3/aws4_request") {
			t.Error("actual issuer lost the frozen input or AWS signing contract")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		issued.Add(1)
		if hold.Load() {
			close(entered)
			<-release
		}
		_, _ = fmt.Fprintf(w, `<AssumeRoleResponse><AssumeRoleResult><Credentials><AccessKeyId>synthetic-execution-key</AccessKeyId><SecretAccessKey>synthetic-execution-secret</SecretAccessKey><SessionToken>synthetic-execution-session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`, time.Now().UTC().Add(15*time.Minute).Format(time.RFC3339))
	}))
	defer sts.Close()
	roots := x509.NewCertPool()
	roots.AddCert(sts.Certificate())
	input := f.request.Admission.Snapshot.Input.Object
	issuer, err := storagecredentials.New(storagecredentials.Config{Endpoint: sts.URL, Region: "us-east-1", RootCAs: roots, Timeout: 5 * time.Second,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic-restricted-owner", SecretAccessKey: "synthetic-restricted-secret"}, nil
		}),
		InputConnectionID: input.StorageConnectionID, InputBucket: input.Bucket, PublicationConnectionID: f.request.Admission.Snapshot.PublicationScope.StorageConnectionID, PublicationBucket: f.request.Admission.Snapshot.PublicationScope.Bucket, ModelConnectionID: "model-store-v1", ModelBucket: "model-models",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	pool := postgres.Prepare(t)()
	defer pool.Close()
	_, client, facts, _, _ := bootstrapFixtureWithPool(t, f, pool, issuer)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request := &modeldevv1.GetStorageCredentialsRequest{Context: f.stepContext("prepare")}
	call := bootstrapCall(ctx, f, "synthetic-bound-prepare")
	if response, err := client.GetStorageCredentials(call, request); err == nil || response != nil || issued.Load() != 0 {
		t.Fatal("credential issuance invented missing Run authority")
	}
	if _, err := client.BeginExecution(call, &modeldevv1.BeginExecutionRequest{Context: request.Context}); err != nil {
		t.Fatal("establish real durable current Run authority", err)
	}
	for range 2 {
		response, err := client.GetStorageCredentials(call, request)
		if err != nil || response.GetCredentials().GetObjectKey() != input.Key || response.GetCredentials().GetObjectPrefix() != "" || response.GetCredentials().GetSessionToken() != "synthetic-execution-session" || response.GetIdentity().GetExecutionId() != f.request.Admission.ExecutionID {
			t.Fatalf("authenticated execution must issue and refresh its fixed input session: %v", err)
		}
	}
	if issued.Load() != 2 {
		t.Fatal("the same scope was not sent on renewal")
	}
	for _, mode := range []string{"other tenant", "other execution", "wrong token", "training step"} {
		bad := proto.Clone(request).(*modeldevv1.GetStorageCredentialsRequest)
		badCall := call
		switch mode {
		case "other tenant":
			badCall = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer synthetic-bound-prepare", "x-ani-tenant-id", "99999999-aaaa-4bbb-8ccc-666666666666"))
		case "other execution":
			bad.Context.Identity.ExecutionId = "99999999-aaaa-4bbb-8ccc-666666666666"
		case "wrong token":
			badCall = bootstrapCall(ctx, f, "invalid-token")
		case "training step":
			bad.Context = f.stepContext("train-wait")
		}
		if response, err := client.GetStorageCredentials(badCall, bad); err == nil || response != nil || issued.Load() != 2 {
			t.Fatalf("%s obtained a storage session", mode)
		}
	}
	hold.Store(true)
	type credentialResult struct {
		response *modeldevv1.GetStorageCredentialsResponse
		err      error
	}
	inflight := make(chan credentialResult, 1)
	go func() {
		response, err := client.GetStorageCredentials(call, request)
		inflight <- credentialResult{response: response, err: err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("fresh issuance did not reach the external STS boundary")
	}
	if _, err := client.RequestExecutionClose(bootstrapCall(ctx, f, "synthetic-bound-close"), &modeldevv1.RequestExecutionCloseRequest{Context: f.stepContext("close"), Reason: modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED}); err != nil {
		close(release)
		t.Fatal("persist the actual execution close fence", err)
	}
	close(release)
	select {
	case result := <-inflight:
		if result.err == nil || result.response != nil {
			t.Fatal("STS call overlapping a persisted close returned a writer credential")
		}
	case <-ctx.Done():
		t.Fatal("credential issuance did not resolve after the close fence")
	}
	state, err := facts.GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || state.CloseGeneration == 0 {
		t.Fatal("close request did not persist the creation and refresh fence", err)
	}
	if response, err := client.GetStorageCredentials(call, request); err == nil || response != nil || issued.Load() != 3 {
		t.Fatal("closing execution refreshed its temporary storage credentials")
	}
}
