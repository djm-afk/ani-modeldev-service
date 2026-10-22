//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type mainFlowQuery struct {
	modeldevv1.ModelDevQueryServiceClient
	address      string
	certificates commandtls.Certificates
}

func startMainFlowQuery(t *testing.T, pool *pgxpool.Pool, store *s3.Client, admission biz.Admission, resolvers ...modeldevv1.ModelDevAdmissionServiceServer) *mainFlowQuery {
	return startMainFlowQueryWithLogs(t, pool, store, admission, nil, resolvers...)
}

func startMainFlowQueryWithLogs(t *testing.T, pool *pgxpool.Pool, store *s3.Client, admission biz.Admission, logs biz.TrainingLogReader, resolvers ...modeldevv1.ModelDevAdmissionServiceServer) *mainFlowQuery {
	t.Helper()
	certs := commandtls.NewForServer(t, "ani-modeldev-service")
	repository := execution.New(pool)
	handler := service.NewQuery(repository, objectstore.NewDownloadSigner(store, admission.Snapshot.PublicationScope.StorageConnectionID), logs)
	var resolver modeldevv1.ModelDevAdmissionServiceServer
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	listener, err := server.NewGovernanceQueryServer(&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(5 * time.Second)}, server.CommandTLS{Certificate: certs.Server, ClientCAs: certs.Roots, GovernanceDNSName: commandtls.GovernanceDNSName}, service.NewCommand(repository), resolver, handler)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := listener.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- listener.Start(context.Background()) }()
	connection, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: certs.Roots, ServerName: "ani-modeldev-service", Certificates: []tls.Certificate{certs.Governance}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := listener.Stop(ctx); err != nil {
			t.Error(err)
		}
		<-done
	})
	return &mainFlowQuery{ModelDevQueryServiceClient: modeldevv1.NewModelDevQueryServiceClient(connection), address: endpoint.Host, certificates: certs}
}

func queryCall(ctx context.Context, admission biz.Admission, method string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", admission.TenantID, "x-ani-actor", "governance:user:9001", "x-ani-request-id", "99999999-1111-4222-8333-444444444444", "x-ani-authorized-method", method, "x-ani-data-scope", "tenant-all"))
}

func assertQueryAccess(t *testing.T, ctx context.Context, client modeldevv1.ModelDevQueryServiceClient, admission biz.Admission) {
	t.Helper()
	method := modeldevv1.ModelDevQueryService_GetExecution_FullMethodName
	request := &modeldevv1.GetExecutionRequest{ExecutionId: admission.ExecutionID}
	got, err := client.GetExecution(queryCall(ctx, admission, method), request)
	if err != nil || got.GetExecution().GetIdentity().GetExecutionId() != admission.ExecutionID || got.GetExecution().GetName() != admission.Intent.Name {
		t.Fatalf("QUERY_MAIN_FLOW_NOT_IMPLEMENTED: currently authorized reader cannot read durable execution: %v", err)
	}
	// A different current authorized actor may read; historical admission actor
	// is audit data. Missing or wrong per-method delegation cannot read.
	for _, candidate := range []context.Context{ctx, queryCall(ctx, admission, modeldevv1.ModelDevQueryService_AuthorizeArtifactDownload_FullMethodName)} {
		if _, err := client.GetExecution(candidate, request); status.Code(err) != codes.Unauthenticated && status.Code(err) != codes.PermissionDenied {
			t.Fatalf("query delegation bypass: %v", err)
		}
	}
	other := admission
	other.TenantID = "99999999-aaaa-4bbb-8ccc-111111111111"
	if _, err := client.GetExecution(queryCall(ctx, other, method), request); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant execution exposed: %v", err)
	}
	absent := &modeldevv1.AuthorizeArtifactDownloadRequest{ArtifactId: "eeeeeeee-1111-4222-8333-444444444444"}
	if _, err := client.AuthorizeArtifactDownload(queryCall(ctx, admission, modeldevv1.ModelDevQueryService_AuthorizeArtifactDownload_FullMethodName), absent); status.Code(err) != codes.NotFound {
		t.Fatalf("unpublished/unknown artifact not hidden: %v", err)
	}
}

func assertPublishedQueryBoundaries(t *testing.T, ctx context.Context, client modeldevv1.ModelDevQueryServiceClient, admission biz.Admission, files []biz.PublishedRuntimeFile) {
	t.Helper()
	other := admission
	other.TenantID = "99999999-aaaa-4bbb-8ccc-111111111111"
	method := modeldevv1.ModelDevQueryService_AuthorizeArtifactDownload_FullMethodName
	if _, err := client.AuthorizeArtifactDownload(queryCall(ctx, other, method), &modeldevv1.AuthorizeArtifactDownloadRequest{ArtifactId: files[0].ArtifactID}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant download disclosed publication: %v", err)
	}
	method = modeldevv1.ModelDevQueryService_ListExecutionArtifacts_FullMethodName
	if _, err := client.ListExecutionArtifacts(queryCall(ctx, other, method), &modeldevv1.ListExecutionArtifactsRequest{ExecutionId: admission.ExecutionID}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant artifact list disclosed publication: %v", err)
	}
	seen := map[string]bool{}
	for pageIndex := uint32(1); ; pageIndex++ {
		current, size := pageIndex, uint32(1)
		page, err := client.ListExecutionArtifacts(queryCall(ctx, admission, method), &modeldevv1.ListExecutionArtifactsRequest{ExecutionId: admission.ExecutionID, Page: &modeldevv1.PageRequest{Page: &current, PageSize: &size}})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.GetArtifacts()) == 0 {
			break
		}
		if len(page.GetArtifacts()) != 1 || seen[page.Artifacts[0].ArtifactId] {
			t.Fatal("artifact pagination lost stable bounded membership")
		}
		seen[page.Artifacts[0].ArtifactId] = true
		if pageIndex > uint32(len(files))+1 {
			t.Fatal("artifact pagination did not terminate")
		}
	}
	if len(seen) != len(files) {
		t.Fatal("artifact pagination omitted publication files")
	}
	view, err := client.GetExecution(queryCall(ctx, admission, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName), &modeldevv1.GetExecutionRequest{ExecutionId: admission.ExecutionID})
	if err != nil || view.GetExecution().GetStates().GetCloseState() != modeldevv1.CloseState_CLOSE_STATE_CLOSED || view.GetExecution().GetStates().GetDeliveryState() != modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED {
		t.Fatal("query lost durable closed/published states")
	}
	t.Log("QUERY_MAIN_FLOW: current delegated reader; durable CLOSED/PUBLISHED; bounded fixed-version signed GET; cross-tenant denied; no historical actor grant")
}

// An optional cross-repository test consumer uses real BFF authentication and
// authorizes every file. Only synthetic TLS references leave this process; no
// storage credential or signed URL is included in the private handshake.
func consumeThroughGovernance(t *testing.T, ctx context.Context, f *completeFixture, query *mainFlowQuery, publication *biz.RuntimePublication, independent string) string {
	t.Helper()
	path := os.Getenv("ANI_MODELDEV_QUERY_HANDSHAKE")
	if path == "" {
		return independent
	}
	output := os.Getenv("ANI_MODELDEV_QUERY_OUTPUT_DIR")
	if !filepath.IsAbs(path) || !filepath.IsAbs(output) {
		t.Fatal("query consumer requires absolute private paths")
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("query handshake directory must be private")
	}
	materials := query.certificates.WriteServerFiles(t)
	privateKey, err := x509.MarshalPKCS8PrivateKey(query.certificates.Governance.PrivateKey)
	if err != nil {
		t.Fatal("synthetic query key encoding failed")
	}
	clientCert, clientKey, storageCA := filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key"), filepath.Join(dir, "storage-ca.pem")
	for name, block := range map[string]*pem.Block{clientCert: {Type: "CERTIFICATE", Bytes: query.certificates.Governance.Certificate[0]}, clientKey: {Type: "PRIVATE KEY", Bytes: privateKey}, storageCA: {Type: "CERTIFICATE", Bytes: f.storage.Certificate().Raw}} {
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("cannot reserve private query material")
		}
		_, writeErr := file.Write(pem.EncodeToMemory(block))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot write private query material")
		}
	}
	var model biz.PublishedRuntimeFile
	for _, file := range publication.Files {
		if file.File.RelativePath == "model.pt" {
			model = file
		}
	}
	if model.ArtifactID == "" {
		t.Fatal("no published checkpoint")
	}
	data, err := json.Marshal(map[string]any{"schema": "ani.cpu-p01.governance-query-fixture.v1", "address": query.address, "tls": map[string]string{"ca_file": materials.CAFile, "cert_file": clientCert, "key_file": clientKey}, "resource_tenant_id": f.request.Admission.TenantID, "execution_id": f.request.Admission.ExecutionID, "artifact": map[string]any{"artifact_id": model.ArtifactID, "filename": model.File.RelativePath, "size_bytes": model.File.SizeBytes, "sha256": model.File.SHA256}, "storage_ca_file": storageCA})
	if err != nil {
		t.Fatal("query handshake encoding failed")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("query handshake already exists or unavailable")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("query handshake write failed")
	}
	t.Log("QUERY_BFF_HANDSHAKE_READY: real published bytes and authenticated query handler available")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Governance query consumer did not finish")
		case <-ticker.C:
		}
	}
	for _, file := range publication.Files {
		data, err := os.ReadFile(filepath.Join(output, file.File.RelativePath))
		if err != nil || int64(len(data)) != file.File.SizeBytes || completeHash(data) != file.File.SHA256 {
			t.Fatal("BFF consumer did not independently retrieve the verified files")
		}
	}
	t.Log("QUERY_BFF_DOWNLOADED: actual BFF consumer retrieved all published files; preparing independent CPU reload")
	return output
}
