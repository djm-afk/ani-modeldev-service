// Package storagecredentials narrows owner-approved RustFS permissions into
// temporary credentials for one authenticated execution and pipeline step.
package storagecredentials

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var ErrUnavailable = errors.New("execution storage credentials unavailable")

type Config struct {
	Endpoint                string
	Region                  string
	RootCAs                 *x509.CertPool
	Credentials             aws.CredentialsProvider
	InputConnectionID       string
	InputBucket             string
	PublicationConnectionID string
	PublicationBucket       string
	ModelConnectionID       string
	ModelBucket             string
	Timeout                 time.Duration
}

type Issuer struct {
	config Config
	client *http.Client
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var regionName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func New(config Config) (*Issuer, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawPath != "" || (endpoint.Path != "" && endpoint.Path != "/") ||
		!regionName.MatchString(config.Region) || config.RootCAs == nil || config.Credentials == nil || config.Timeout <= 0 || config.Timeout > time.Minute || !biz.ValidStorageReference(config.InputConnectionID) || !bucketName.MatchString(config.InputBucket) || !biz.ValidStorageReference(config.PublicationConnectionID) || !bucketName.MatchString(config.PublicationBucket) || !biz.ValidStorageReference(config.ModelConnectionID) || !bucketName.MatchString(config.ModelBucket) {
		return nil, ErrUnavailable
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: config.Timeout}).DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: config.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: config.Timeout, ResponseHeaderTimeout: config.Timeout,
		MaxConnsPerHost: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
	}
	return &Issuer{config: config, client: &http.Client{Transport: transport, Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// IssueForExecution consumes the persisted immutable admission only after its
// caller has reverified current Pod, Run and execution authority.
func (issuer *Issuer) IssueForExecution(ctx context.Context, execution biz.Execution, purpose string) (biz.TemporaryStorageCredentials, error) {
	failure := func() (biz.TemporaryStorageCredentials, error) {
		if ctx != nil && ctx.Err() != nil {
			return biz.TemporaryStorageCredentials{}, ctx.Err()
		}
		return biz.TemporaryStorageCredentials{}, ErrUnavailable
	}
	if issuer == nil || ctx == nil || ctx.Err() != nil || !execution.Snapshot.DeadlineAt.After(time.Now()) || execution.Close != nil || execution.States.Close == biz.CloseStateClosed {
		return failure()
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return failure()
	}
	credentials, actions, resource, ok := issuer.scope(execution, purpose)
	if !ok {
		return failure()
	}
	statement := policyStatement{Effect: "Allow", Action: actions, Resource: []string{resource}}
	object := execution.Snapshot.Input.Object
	if purpose == "prepare-base-model" {
		object = execution.Snapshot.Finetuning.BaseModel.Object
	}
	if (purpose == "prepare" || purpose == "prepare-base-model") && object.VersionID != nil {
		statement.Action = []string{"s3:GetObjectVersion"}
		statement.Condition = map[string]map[string][]string{"StringEquals": {"s3:VersionId": {*object.VersionID}}}
	}
	policy, err := json.Marshal(struct {
		Version   string            `json:"Version"`
		Statement []policyStatement `json:"Statement"`
	}{"2012-10-17", []policyStatement{statement}})
	if err != nil || len(policy) > 2048 {
		return failure()
	}
	owner, err := issuer.config.Credentials.Retrieve(ctx)
	if err != nil || owner.CanExpire || owner.SessionToken != "" || !safeCredential(owner.AccessKeyID) || !safeCredential(owner.SecretAccessKey) {
		return failure()
	}
	values := url.Values{
		"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "DurationSeconds": {"900"},
		"RoleSessionName": {"modeldev-" + execution.ExecutionID + "-" + purpose}, "Policy": {string(policy)},
	}
	encoded := values.Encode()
	endpoint := strings.TrimSuffix(issuer.config.Endpoint, "/") + "/"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encoded))
	if err != nil {
		return failure()
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	digest := sha256.Sum256([]byte(encoded))
	request.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(digest[:]))
	started := time.Now().UTC()
	// RustFS exposes STS through its S3 endpoint. Reuse the pinned AWS SDK's
	// SigV4 implementation with the documented s3 signing service.
	if err := v4.NewSigner().SignHTTP(ctx, owner, request, hex.EncodeToString(digest[:]), "s3", issuer.config.Region, started); err != nil {
		return failure()
	}
	response, err := issuer.client.Do(request)
	if err != nil {
		return failure()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return failure()
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return failure()
	}
	var reply struct {
		XMLName     xml.Name `xml:"AssumeRoleResponse"`
		Credentials struct {
			AccessKeyID     string    `xml:"AccessKeyId"`
			SecretAccessKey string    `xml:"SecretAccessKey"`
			SessionToken    string    `xml:"SessionToken"`
			Expiration      time.Time `xml:"Expiration"`
		} `xml:"AssumeRoleResult>Credentials"`
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&reply); err != nil {
		return failure()
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return failure()
		}
		space, ok := token.(xml.CharData)
		if !ok || strings.TrimSpace(string(space)) != "" {
			return failure()
		}
	}
	value := reply.Credentials
	if !safeCredential(value.AccessKeyID) || !safeCredential(value.SecretAccessKey) || !safeCredential(value.SessionToken) || !value.Expiration.After(time.Now().Add(2*time.Minute)) || value.Expiration.After(started.Add(16*time.Minute)) {
		return failure()
	}
	credentials.AccessKeyID, credentials.SecretAccessKey, credentials.SessionToken, credentials.ExpiresAt = value.AccessKeyID, value.SecretAccessKey, value.SessionToken, value.Expiration.UTC()
	return credentials, nil
}

type policyStatement struct {
	Effect    string                         `json:"Effect"`
	Action    []string                       `json:"Action"`
	Resource  []string                       `json:"Resource"`
	Condition map[string]map[string][]string `json:"Condition,omitempty"`
}

func (issuer *Issuer) scope(execution biz.Execution, purpose string) (biz.TemporaryStorageCredentials, []string, string, bool) {
	var connection, bucket, key, prefix string
	actions := []string{"s3:GetObject", "s3:GetObjectVersion"}
	switch purpose {
	case "prepare":
		input := execution.Snapshot.Input.Object
		connection, bucket, key = input.StorageConnectionID, input.Bucket, input.Key
	case "prepare-base-model":
		if execution.Snapshot.Kind != "FINETUNING" || execution.Snapshot.Finetuning == nil {
			return biz.TemporaryStorageCredentials{}, nil, "", false
		}
		base := execution.Snapshot.Finetuning.BaseModel.Object
		connection, bucket, key = base.StorageConnectionID, base.Bucket, base.Key
	case "publish":
		scope := execution.Snapshot.PublicationScope
		connection, bucket, prefix = scope.StorageConnectionID, scope.Bucket, scope.ApprovedPrefix+"/"+execution.ExecutionID
		actions = append(actions, "s3:PutObject")
	default:
		return biz.TemporaryStorageCredentials{}, nil, "", false
	}
	object := key
	if purpose == "publish" {
		object = prefix
	}
	if !safePolicyKey(object) {
		return biz.TemporaryStorageCredentials{}, nil, "", false
	}
	if !cpup01.CanonicalTenantID(execution.TenantID) || execution.TenantID != execution.Snapshot.IdentityRef.TenantID || execution.ExecutionID != execution.Snapshot.IdentityRef.ExecutionID {
		return biz.TemporaryStorageCredentials{}, nil, "", false
	}
	approvedConnection, approvedBucket, approvedPrefix := issuer.config.InputConnectionID, issuer.config.InputBucket, "inputs/"+execution.TenantID
	switch purpose {
	case "prepare-base-model":
		approvedConnection, approvedBucket, approvedPrefix = issuer.config.ModelConnectionID, issuer.config.ModelBucket, execution.TenantID
	case "publish":
		approvedConnection, approvedBucket, approvedPrefix = issuer.config.PublicationConnectionID, issuer.config.PublicationBucket, "executions/"+execution.TenantID
	}
	if connection != approvedConnection || bucket != approvedBucket || !strings.HasPrefix(object, approvedPrefix+"/") {
		return biz.TemporaryStorageCredentials{}, nil, "", false
	}
	credentials := biz.TemporaryStorageCredentials{StorageConnectionID: connection, Bucket: bucket, ObjectKey: key}
	resource := "arn:aws:s3:::" + bucket + "/" + key
	if purpose == "publish" {
		credentials.ObjectPrefix = prefix + "/"
		resource = "arn:aws:s3:::" + bucket + "/" + credentials.ObjectPrefix + "*"
	}
	return credentials, actions, resource, true
}

func safePolicyKey(value string) bool {
	return biz.ValidStorageKey(value) && !strings.ContainsAny(value, "*?${}")
}

func safeCredential(value string) bool {
	return value != "" && len(value) <= 16384 && !strings.ContainsAny(value, " \t\r\n")
}

func (issuer *Issuer) Close() {
	if issuer != nil && issuer.client != nil {
		issuer.client.CloseIdleConnections()
	}
}

var _ biz.StorageCredentialIssuer = (*Issuer)(nil)

// IssueImportSession issues one attempt-scoped session. It reuses the same
// SigV4 + AssumeRole path as execution issuance but scopes the policy to the
// attempt's exact data and result keys, so an importer can never write another
// attempt's bytes.
func (issuer *Issuer) IssueImportSession(ctx context.Context, request biz.ImportSessionRequest, grant biz.ImportSessionGrant) (biz.ImportSession, error) {
	if issuer == nil || ctx == nil || ctx.Err() != nil {
		return biz.ImportSession{}, ErrUnavailable
	}
	if !biz.ValidStorageReference(grant.StorageConnectionID) || grant.Bucket == "" ||
		!biz.ValidStorageKey(grant.DataKey) || !biz.ValidStorageKey(grant.ResultKey) || grant.DataKey == grant.ResultKey {
		return biz.ImportSession{}, ErrUnavailable
	}
	// The platform input scope is the only boundary this issuer may widen to; a
	// task frozen against another bucket or connection is not issuable here.
	if grant.StorageConnectionID != issuer.config.InputConnectionID || grant.Bucket != issuer.config.InputBucket {
		return biz.ImportSession{}, ErrUnavailable
	}
	tenantPrefix := "inputs/" + request.TenantID
	if !strings.HasPrefix(grant.DataKey, tenantPrefix+"/") || !strings.HasPrefix(grant.ResultKey, tenantPrefix+"/") {
		return biz.ImportSession{}, ErrUnavailable
	}
	statement := policyStatement{
		Effect: "Allow",
		// Same action vocabulary the execution issuer uses for its writable
		// session. Adding multipart-only actions made this store reject the whole
		// policy as unknown, which surfaced as AccessDenied on every PutObject.
		Action: []string{"s3:GetObject", "s3:GetObjectVersion", "s3:PutObject"},
		// Prefix-scoped to this attempt only. An exact-object ARN is not honoured
		// by this store's STS policy engine, which evaluates the prefix form.
		Resource: []string{"arn:aws:s3:::" + grant.Bucket + "/" + attemptPrefix(grant)},
	}
	policy, err := json.Marshal(struct {
		Version   string            `json:"Version"`
		Statement []policyStatement `json:"Statement"`
	}{"2012-10-17", []policyStatement{statement}})
	if err != nil {
		return biz.ImportSession{}, fmt.Errorf("%w: encode import policy: %v", ErrUnavailable, err)
	}
	if len(policy) > 2048 {
		return biz.ImportSession{}, fmt.Errorf("%w: import policy is %d bytes", ErrUnavailable, len(policy))
	}
	owner, err := issuer.config.Credentials.Retrieve(ctx)
	if err != nil {
		return biz.ImportSession{}, fmt.Errorf("%w: retrieve owner credential: %v", ErrUnavailable, err)
	}
	if owner.CanExpire || owner.SessionToken != "" {
		return biz.ImportSession{}, fmt.Errorf("%w: owner credential must be long-lived parent key", ErrUnavailable)
	}
	if !safeCredential(owner.AccessKeyID) || !safeCredential(owner.SecretAccessKey) {
		return biz.ImportSession{}, fmt.Errorf("%w: owner credential is malformed", ErrUnavailable)
	}
	values := url.Values{
		"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "DurationSeconds": {"900"},
		"RoleSessionName": {importSessionName(request)}, "Policy": {string(policy)},
	}
	encoded := values.Encode()
	endpoint := strings.TrimSuffix(issuer.config.Endpoint, "/") + "/"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encoded))
	if err != nil {
		return biz.ImportSession{}, ErrUnavailable
	}
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	digest := sha256.Sum256([]byte(encoded))
	httpRequest.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(digest[:]))
	started := time.Now().UTC()
	if err := v4.NewSigner().SignHTTP(ctx, owner, httpRequest, hex.EncodeToString(digest[:]), "s3", issuer.config.Region, started); err != nil {
		return biz.ImportSession{}, ErrUnavailable
	}
	response, err := issuer.client.Do(httpRequest)
	if err != nil {
		return biz.ImportSession{}, fmt.Errorf("%w: assume role request: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return biz.ImportSession{}, fmt.Errorf("%w: read assume role response: %v", ErrUnavailable, err)
	}
	if response.StatusCode != http.StatusOK {
		// The STS error body names the rejected parameter; keep a bounded prefix.
		return biz.ImportSession{}, fmt.Errorf("%w: assume role status %d: %.200s", ErrUnavailable, response.StatusCode, strings.TrimSpace(string(data)))
	}
	var reply struct {
		XMLName     xml.Name `xml:"AssumeRoleResponse"`
		Credentials struct {
			AccessKeyID     string    `xml:"AccessKeyId"`
			SecretAccessKey string    `xml:"SecretAccessKey"`
			SessionToken    string    `xml:"SessionToken"`
			Expiration      time.Time `xml:"Expiration"`
		} `xml:"AssumeRoleResult>Credentials"`
	}
	if err := xml.Unmarshal(data, &reply); err != nil {
		return biz.ImportSession{}, fmt.Errorf("%w: decode assume role response: %v", ErrUnavailable, err)
	}
	value := reply.Credentials
	if !safeCredential(value.AccessKeyID) || !safeCredential(value.SecretAccessKey) || !safeCredential(value.SessionToken) {
		return biz.ImportSession{}, fmt.Errorf("%w: assume role returned an unusable session", ErrUnavailable)
	}
	if !value.Expiration.After(time.Now().Add(2*time.Minute)) || value.Expiration.After(started.Add(16*time.Minute)) {
		return biz.ImportSession{}, fmt.Errorf("%w: assume role expiry %s is outside the allowed window", ErrUnavailable, value.Expiration.UTC())
	}
	return biz.ImportSession{
		Issued: true, ExpiresAt: value.Expiration.UTC(),
		Credentials: biz.TemporaryStorageCredentials{
			AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey,
			SessionToken: value.SessionToken, ExpiresAt: value.Expiration.UTC(),
			StorageConnectionID: grant.StorageConnectionID, Bucket: grant.Bucket, ObjectKey: grant.DataKey,
		},
	}, nil
}

var _ biz.ImportSessionIssuer = (*Issuer)(nil)

// importSessionName stays within the STS 64-character limit. The task and
// attempt UUIDs are already unique, so a prefix of both keeps the name
// recognisable for operators while guaranteeing uniqueness.
func importSessionName(request biz.ImportSessionRequest) string {
	short := func(value string) string {
		if len(value) <= 8 {
			return value
		}
		return value[:8]
	}
	return "md-import-" + short(request.TaskID) + "-" + short(request.AttemptID)
}

// attemptPrefix is the shared directory of the attempt's two keys, expressed as
// a policy wildcard. This store's STS policy engine evaluates prefix-form
// resources; an exact-object ARN is ignored and the request is denied.
func attemptPrefix(grant biz.ImportSessionGrant) string {
	data, result := grant.DataKey, grant.ResultKey
	index := 0
	for index < len(data) && index < len(result) && data[index] == result[index] {
		index++
	}
	if slash := strings.LastIndexByte(data[:index], '/'); slash >= 0 {
		return data[:slash+1] + "*"
	}
	return data[:index] + "*"
}
