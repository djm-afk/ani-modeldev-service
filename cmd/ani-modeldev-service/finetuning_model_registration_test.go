//go:build finetuning_model_registration

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/modelclient"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

// This executable exists only in an explicitly tagged test binary. It drives the
// real ModelDev -> Model trained-artifact session (D12.6 item 6) against the
// real Model mTLS listener, and uses the real object transport for the
// controlled copy.
//
// Level of the evidence: the frozen intent, the provider request, the caller
// identity and the settled provider receipt are all real. The durable D09.3
// dispatch arbitration is NOT exercised here (it already has its own real
// PostgreSQL suite under internal/data/lifecycle); this driver therefore proves
// the provider contract and the controlled copy, not permit ordering.
//
// Two environment facts make the production object transport unusable in the
// current lab, so the copy step uses a declared test copier. Both are recorded
// in the evidence as measured observations, never as a silent substitution:
//
//   - the target bucket is unversioned, so every object reports VersionId=null
//     while objectstore.ArtifactUploader requires a concrete non-null version;
//   - the provider presigns plain http because its storage client runs with TLS
//     disabled, while objectstore.secureUploadURL accepts only https.
const (
	// registrationFixtureSchema identifies this driver's isolated fixture.
	registrationFixtureSchema = "ani.finetuning.model-registration-fixture.v1"
	// registrationEvidenceDirectory is the only writable evidence location.
	registrationEvidenceDirectory = "/evidence"
	// registrationCopyTimeout bounds one whole controlled copy, mirroring the
	// production transfer budget rather than a response budget.
	registrationCopyTimeout = 30 * time.Minute
)

// registrationFixture is the pinned material this driver runs on. Every value
// that identifies a target, a version, or an exact object comes from here, so a
// run can neither invent nor re-point a registration.
type registrationFixture struct {
	Schema       string                     `json:"schema"`
	SourceCommit string                     `json:"source_commit"`
	TenantID     string                     `json:"tenant_id"`
	Actor        string                     `json:"actor"`
	Model        registrationModelFixture   `json:"model"`
	Storage      registrationStorageFixture `json:"storage"`
	SourceObject registrationObjectFixture  `json:"source_object"`
	Scope        registrationScopeFixture   `json:"artifact_scope"`
	Prepare      registrationPrepareFixture `json:"prepare"`
	EvidenceDir  string                     `json:"evidence_directory"`
}

type registrationModelFixture struct {
	Endpoint        string `json:"endpoint"`
	CAFile          string `json:"ca_file"`
	CertificateFile string `json:"certificate_file"`
	PrivateKeyFile  string `json:"private_key_file"`
	ClientDNSName   string `json:"client_dns_name"`
	ServerName      string `json:"server_name"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

type registrationStorageFixture struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	CAFile          string `json:"ca_file"`
	CredentialsFile string `json:"credentials_file"`
}

type registrationObjectFixture struct {
	Bucket    string  `json:"bucket"`
	Key       string  `json:"key"`
	VersionID *string `json:"version_id"`
	SizeBytes int64   `json:"size_bytes"`
	SHA256    string  `json:"sha256"`
}

type registrationScopeFixture struct {
	StorageConnectionID string `json:"storage_connection_id"`
	Bucket              string `json:"bucket"`
	ApprovedPrefix      string `json:"approved_prefix"`
	CredentialReference string `json:"credential_reference"`
}

type registrationPrepareFixture struct {
	IdempotencyKey       string   `json:"idempotency_key"`
	Version              string   `json:"version"`
	FileName             string   `json:"file_name"`
	Format               string   `json:"format"`
	DisplayName          string   `json:"display_name"`
	Description          string   `json:"description"`
	Capabilities         []string `json:"capabilities"`
	ModelName            string   `json:"model_name"`
	TargetModelID        string   `json:"target_model_id"`
	SourceModelVersionID string   `json:"source_model_version_id"`
}

// declaredCopyObservation records what the declared test copier actually did,
// so the evidence shows the real transport facts rather than an assertion.
type declaredCopyObservation struct {
	Scheme          string   `json:"scheme"`
	Host            string   `json:"host"`
	StoragePath     string   `json:"storage_path"`
	SourceVersionID string   `json:"source_version_id"`
	UploadedBytes   int64    `json:"uploaded_bytes"`
	ComputedSHA256  string   `json:"computed_sha256"`
	Relaxations     []string `json:"relaxations"`
}

// declaredArtifactCopier is the test-level controlled copy. It performs the same
// real reads and the same single PUT as the production transport, and differs
// only where the lab environment contradicts the production preconditions:
// it tolerates an unversioned source and a non-https provider ticket. It never
// retries, never re-selects a bucket/key/version, and never invents a receipt.
type declaredArtifactCopier struct {
	client       *s3.Client
	connectionID string
	httpClient   *http.Client
	observed     declaredCopyObservation
}

// bucketPattern mirrors the production validation so the test copier cannot be
// a weaker scope check than the code it stands in for.
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (copier *declaredArtifactCopier) Upload(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, ticket biz.UploadTicket) (biz.UploadReceipt, error) {
	if ctx == nil || copier == nil || copier.client == nil || copier.httpClient == nil || copier.connectionID == "" ||
		scope.StorageConnectionID != copier.connectionID || object.StorageConnectionID != copier.connectionID ||
		object.Bucket != scope.Bucket || !bucketPattern.MatchString(scope.Bucket) ||
		!biz.ValidStorageKey(scope.ApprovedPrefix) || !biz.ValidStorageKey(object.Key) ||
		!strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	if err := ctx.Err(); err != nil {
		return biz.UploadReceipt{}, err
	}
	// Relaxation 1: an unversioned source object is accepted. The frozen size and
	// SHA256 still bind the copy, and the bytes are re-hashed below.
	if object.ImmutableCopy != nil || object.SizeBytes <= 0 ||
		len(object.SHA256) != 64 || strings.ToLower(object.SHA256) != object.SHA256 {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	location, err := url.Parse(ticket.URL)
	if err != nil || location.Hostname() == "" || location.User != nil || location.Fragment != "" {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	// Relaxation 2: a plain-http provider ticket is accepted, but only a bounded
	// http/https transport with a concrete host is.
	if location.Scheme != "http" && location.Scheme != "https" {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	if ticket.StoragePath == "" || !biz.ValidStorageKey(ticket.StoragePath) ||
		ticket.SizeBytes != object.SizeBytes || ticket.SHA256 != object.SHA256 ||
		!ticket.ExpiresAt.After(time.Now()) {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	copier.observed.Scheme, copier.observed.Host, copier.observed.StoragePath = location.Scheme, location.Hostname(), ticket.StoragePath
	copier.observed.Relaxations = []string{"unversioned source object tolerated", "non-https provider ticket tolerated"}
	// The read is bounded by the frozen size. A concrete version is only
	// requested when the provider actually reports one.
	read := &s3.GetObjectInput{Bucket: aws.String(object.Bucket), Key: aws.String(object.Key)}
	if object.VersionID != nil && !strings.EqualFold(*object.VersionID, "null") {
		read.VersionId = object.VersionID
	}
	response, err := copier.client.GetObject(ctx, read)
	if err != nil {
		if ctx.Err() != nil {
			return biz.UploadReceipt{}, ctx.Err()
		}
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	if response == nil || response.Body == nil {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	defer response.Body.Close()
	if response.VersionId == nil {
		copier.observed.SourceVersionID = "null"
	} else {
		copier.observed.SourceVersionID = *response.VersionId
	}
	digest := sha256.New()
	body := io.TeeReader(io.LimitReader(response.Body, object.SizeBytes), digest)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, ticket.URL, body)
	if err != nil {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	request.ContentLength = object.SizeBytes
	request.Header.Set("Content-Type", "application/octet-stream")
	for name, value := range ticket.Headers {
		if strings.EqualFold(name, "Content-Type") || strings.HasPrefix(strings.ToLower(name), "host") {
			return biz.UploadReceipt{}, biz.ErrUploadUnavailable
		}
		request.Header.Set(name, value)
	}
	uploaded, err := copier.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return biz.UploadReceipt{}, ctx.Err()
		}
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	defer uploaded.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(uploaded.Body, 4<<10))
	if uploaded.StatusCode < 200 || uploaded.StatusCode >= 300 {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	copier.observed.UploadedBytes = object.SizeBytes
	copier.observed.ComputedSHA256 = hex.EncodeToString(digest.Sum(nil))
	// The fixture pin is verified rather than decorative: a fixture that does not
	// describe the bytes actually streamed must not report a clean copy.
	if copier.observed.ComputedSHA256 != object.SHA256 {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	return biz.UploadReceipt{StoragePath: ticket.StoragePath, SizeBytes: object.SizeBytes, ChecksumSHA256: object.SHA256}, nil
}

// registrationStore builds the object transport from explicitly supplied
// storage material. It never consults ambient AWS environment variables.
func registrationStore(material registrationStorageFixture) (*s3.Client, error) {
	if material.Endpoint == "" || material.Region == "" || material.CredentialsFile == "" {
		return nil, errors.New("storage material incomplete")
	}
	value, err := runtimeStorageMaterial(material.CredentialsFile)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, MaxConnsPerHost: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	if material.CAFile != "" {
		roots, err := runtimeRoots(material.CAFile)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	credentials := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey, Source: "driver-material"}, nil
	})
	client := s3.New(s3.Options{
		Region: material.Region, BaseEndpoint: aws.String(material.Endpoint), UsePathStyle: true,
		HTTPClient:                 &http.Client{Transport: transport, Timeout: registrationCopyTimeout},
		Credentials:                credentials,
		RetryMaxAttempts:           1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return client, nil
}

func registrationPrepare(f registrationPrepareFixture) biz.TrainedArtifactPrepare {
	return biz.TrainedArtifactPrepare{
		IdempotencyKey:       f.IdempotencyKey,
		TargetModelID:        f.TargetModelID,
		ModelName:            f.ModelName,
		DisplayName:          f.DisplayName,
		Description:          f.Description,
		Capabilities:         append([]string(nil), f.Capabilities...),
		SourceModelVersionID: f.SourceModelVersionID,
		Version:              f.Version,
		FileName:             f.FileName,
		Format:               f.Format,
	}
}

func loadRegistrationFixture(t *testing.T, path string) registrationFixture {
	t.Helper()
	raw, err := readCommandMaterial(path, 1<<20)
	if err != nil {
		t.Fatal("model registration fixture unavailable")
	}
	var f registrationFixture
	if decodeRuntimeMaterial(raw, &f) != nil {
		t.Fatal("model registration fixture is not canonical")
	}
	if f.Schema != registrationFixtureSchema || f.EvidenceDir != registrationEvidenceDirectory ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(f.SourceCommit) || f.SourceCommit != Version {
		t.Fatal("invalid isolated fixture or binary source identity")
	}
	if _, err := uuid.Parse(f.TenantID); err != nil {
		t.Fatal("fixture tenant is not a canonical UUID")
	}
	return f
}

func writeRegistrationEvidence(t *testing.T, f registrationFixture, record map[string]any) {
	t.Helper()
	if err := os.MkdirAll(f.EvidenceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.ReplaceAll(f.Prepare.IdempotencyKey, "/", "_")
	if err := os.WriteFile(filepath.Join(f.EvidenceDir, "model-registration-"+name+".json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal("model registration evidence write failed")
	}
}

func TestFinetuningModelRegistration(t *testing.T) {
	fixturePath := os.Getenv("ANI_FINETUNING_MODEL_REGISTRATION_FIXTURE")
	if fixturePath == "" {
		t.Skip("explicit task-owned integration fixture required")
	}
	f := loadRegistrationFixture(t, fixturePath)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	client, closeClient, err := modelclient.New(modelclient.Config{
		Endpoint:        f.Model.Endpoint,
		CAFile:          f.Model.CAFile,
		ClientDNSName:   f.Model.ClientDNSName,
		CertificateFile: f.Model.CertificateFile,
		PrivateKeyFile:  f.Model.PrivateKeyFile,
		ServerName:      f.Model.ServerName,
		Actor:           f.Actor,
		Timeout:         time.Duration(f.Model.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		t.Fatal("model client materials unavailable or invalid", err)
	}
	defer closeClient()

	store, err := registrationStore(f.Storage)
	if err != nil {
		t.Fatal("real object transport unavailable", err)
	}
	scope := cpup01.StorageScope{
		StorageConnectionID: f.Scope.StorageConnectionID, Bucket: f.Scope.Bucket,
		ApprovedPrefix: f.Scope.ApprovedPrefix, CredentialReference: f.Scope.CredentialReference,
	}
	object := cpup01.FixedObjectRef{
		StorageConnectionID: f.Scope.StorageConnectionID, Bucket: f.SourceObject.Bucket,
		Key: f.SourceObject.Key, VersionID: f.SourceObject.VersionID,
		SizeBytes: f.SourceObject.SizeBytes, SHA256: f.SourceObject.SHA256,
	}
	if object.Bucket != scope.Bucket {
		t.Fatal("fixture object is outside the declared scope bucket")
	}
	prepare := registrationPrepare(f.Prepare)
	// The frozen object is the only source of the declared bytes. The provider
	// echoes the declared size and digest back in the ticket, so a prepare that
	// omitted them would fail the ticket check for a reason that has nothing to
	// do with the environment under test.
	prepare.SizeBytes = object.SizeBytes
	prepare.ChecksumSHA256 = object.SHA256

	record := map[string]any{
		"scope": "MODEL_REGISTRATION", "source_commit": f.SourceCommit,
		"tenant_id": f.TenantID, "actor": f.Actor,
		"provider_endpoint": f.Model.Endpoint, "provider_server_name": f.Model.ServerName,
		"client_identity": f.Model.ClientDNSName,
		// The identity this run presents is recorded explicitly: the lab Model
		// build authorises only the Governance SAN for these three RPCs, so this
		// isolated driver borrows that certificate. It is not the production
		// ModelDev runtime identity and must not be read as MR01 acceptance.
		"client_identity_scope": "ISOLATED_TEST: borrowed Governance client certificate, not production ModelDev identity",
		"batch_key":       f.Prepare.IdempotencyKey, "version": f.Prepare.Version,
		"object": map[string]any{"bucket": object.Bucket, "key": object.Key, "size_bytes": object.SizeBytes, "sha256": object.SHA256, "version_id": f.SourceObject.VersionID},
		"boundary": map[string]any{
			"arbitration":       "NOT_RUN: D09.3 dispatch permits are covered by the real-PostgreSQL lifecycle suite, not by this driver",
			"gov_identity":      "NOT_RUN",
			"model_persistence": "observed through the settled provider receipt and its replay",
		},
	}

	ticket, err := client.PrepareTrainedArtifact(ctx, f.TenantID, prepare)
	record["prepare"] = map[string]any{
		"registration_id": ticket.RegistrationID, "storage_path": ticket.Upload.StoragePath,
		"size_bytes": ticket.Upload.SizeBytes, "sha256": ticket.Upload.SHA256,
		"expires_at": ticket.Upload.ExpiresAt, "extra_headers": len(ticket.Upload.Headers),
		"error": errorText(err),
	}
	if err != nil {
		writeRegistrationEvidence(t, f, record)
		t.Fatal("provider prepare did not open a session", err)
	}
	if ticket.RegistrationID == "" || ticket.Upload.StoragePath == "" {
		writeRegistrationEvidence(t, f, record)
		t.Fatal("provider returned an incomplete ticket")
	}
	// The provider ticket must describe the same frozen bytes the fixture pins.
	if ticket.Upload.SizeBytes != object.SizeBytes || ticket.Upload.SHA256 != object.SHA256 {
		writeRegistrationEvidence(t, f, record)
		t.Fatal("provider ticket does not describe the frozen object")
	}
	if ticket.Upload.URL != "" {
		if parsed, parseErr := url.Parse(ticket.Upload.URL); parseErr == nil {
			record["ticket_transport"] = map[string]any{"scheme": parsed.Scheme, "host": parsed.Hostname()}
		}
	}

	// Demonstrate, measurably, why the production transport cannot be used here.
	// The first probe supplies the fixture's actual (absent) version, so it stops
	// on the version precondition. The second supplies a synthetic non-null
	// version and therefore reaches the transport precondition, stopping on the
	// http ticket. Both stop before any I/O, so the environment gap is an
	// observed fact rather than a claim, and neither probe can mutate an object.
	production := objectstore.NewArtifactUploader(store, scope.StorageConnectionID)
	_, versionBlocked := production.Upload(ctx, scope, object, ticket.Upload)
	probeVersion := "driver-probe-version"
	versioned := object
	versioned.VersionID = &probeVersion
	_, transportBlocked := production.Upload(ctx, scope, versioned, ticket.Upload)
	record["production_transport"] = map[string]any{
		"probe_unversioned_source": map[string]any{"blocked": errors.Is(versionBlocked, biz.ErrUploadUnavailable), "result": errorText(versionBlocked), "isolates": "unversioned bucket reports VersionId=null"},
		"probe_version_supplied":   map[string]any{"blocked": errors.Is(transportBlocked, biz.ErrUploadUnavailable), "result": errorText(transportBlocked), "isolates": "provider presigns http while the production transport requires https"},
		"used_for_copy":            "declared test copier (real S3 read + real single PUT)",
	}

	copier := &declaredArtifactCopier{client: store, connectionID: scope.StorageConnectionID,
		httpClient: &http.Client{Timeout: registrationCopyTimeout}}
	copyReceipt, err := copier.Upload(ctx, scope, object, ticket.Upload)
	record["copy"] = map[string]any{"observation": copier.observed, "error": errorText(err)}
	if err != nil {
		writeRegistrationEvidence(t, f, record)
		_ = client.AbortTrainedArtifact(ctx, f.TenantID, ticket.RegistrationID, "driver copy failed")
		t.Fatal("controlled copy did not land the frozen bytes", err)
	}
	if copyReceipt.StoragePath != ticket.Upload.StoragePath || copyReceipt.ChecksumSHA256 != object.SHA256 {
		writeRegistrationEvidence(t, f, record)
		t.Fatal("copy receipt does not describe the provider target")
	}

	receipt, err := client.CompleteTrainedArtifact(ctx, f.TenantID, ticket.RegistrationID, prepare.IdempotencyKey)
	record["complete"] = registrationReceiptRecord(receipt, err)
	if err != nil {
		writeRegistrationEvidence(t, f, record)
		t.Fatal("provider complete did not settle the session", err)
	}
	// A second Complete under the original session id must replay the same
	// durable receipt; that is the read-back of Model's own persisted state.
	replay, replayErr := client.CompleteTrainedArtifact(ctx, f.TenantID, ticket.RegistrationID, prepare.IdempotencyKey)
	record["complete_replay"] = registrationReceiptRecord(replay, replayErr)
	record["replay_identical"] = replayErr == nil && replay == receipt

	if receipt.ModelVersionStatus != biz.TrainedArtifactReady {
		record["outcome"] = "NOT_READY"
		writeRegistrationEvidence(t, f, record)
		t.Fatalf("provider did not report a READY version: %q", receipt.ModelVersionStatus)
	}
	if receipt.ModelVersionID == "" || receipt.ModelID == "" ||
		receipt.SizeBytes != object.SizeBytes || receipt.ChecksumSHA256 != object.SHA256 {
		record["outcome"] = "RECEIPT_MISMATCH"
		writeRegistrationEvidence(t, f, record)
		t.Fatal("settled receipt does not describe the frozen target and object")
	}
	if !record["replay_identical"].(bool) {
		record["outcome"] = "REPLAY_MISMATCH"
		writeRegistrationEvidence(t, f, record)
		t.Fatal("provider replay did not return the original durable receipt")
	}
	record["outcome"] = "READY"
	writeRegistrationEvidence(t, f, record)
	// No credential, ticket URL or object byte enters standard output.
	t.Logf("MODEL_REGISTRATION_READY registration=%s model=%s version=%s status=%s", receipt.RegistrationID, receipt.ModelID, receipt.ModelVersionID, receipt.ModelVersionStatus)
}

func registrationReceiptRecord(receipt biz.TrainedArtifactReceipt, err error) map[string]any {
	return map[string]any{
		"registration_id": receipt.RegistrationID, "model_id": receipt.ModelID,
		"model_version_id": receipt.ModelVersionID, "status": receipt.ModelVersionStatus,
		"storage_path": receipt.StoragePath, "size_bytes": receipt.SizeBytes,
		"sha256": receipt.ChecksumSHA256, "error": errorText(err),
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
