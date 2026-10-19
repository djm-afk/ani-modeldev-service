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
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var ErrUnavailable = errors.New("execution storage credentials unavailable")

type Binding struct {
	TenantID     string `json:"tenant_id"`
	Purpose      string `json:"purpose"`
	ConnectionID string `json:"connection_id"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix"`
}

type Config struct {
	Endpoint    string
	Region      string
	RootCAs     *x509.CertPool
	Credentials aws.CredentialsProvider
	Bindings    []Binding
	Timeout     time.Duration
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
		!regionName.MatchString(config.Region) || config.RootCAs == nil || config.Credentials == nil || config.Timeout <= 0 || config.Timeout > time.Minute || len(config.Bindings) == 0 || len(config.Bindings) > 64 {
		return nil, ErrUnavailable
	}
	seen := make(map[Binding]bool)
	for _, binding := range config.Bindings {
		id, err := uuid.Parse(binding.TenantID)
		if err != nil || id == uuid.Nil || id.String() != binding.TenantID || (binding.Purpose != "prepare" && binding.Purpose != "prepare-base-model" && binding.Purpose != "publish") ||
			!biz.ValidStorageReference(binding.ConnectionID) || !bucketName.MatchString(binding.Bucket) || !safePolicyKey(binding.Prefix) || seen[binding] {
			return nil, ErrUnavailable
		}
		seen[binding] = true
	}
	config.Bindings = append([]Binding(nil), config.Bindings...)
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
	if issuer == nil || ctx == nil || ctx.Err() != nil || !execution.Snapshot.DeadlineAt.After(time.Now()) {
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
	for _, binding := range issuer.config.Bindings {
		if binding.TenantID != execution.TenantID || binding.Purpose != purpose || binding.ConnectionID != connection || binding.Bucket != bucket || !strings.HasPrefix(object, binding.Prefix+"/") {
			continue
		}
		credentials := biz.TemporaryStorageCredentials{StorageConnectionID: connection, Bucket: bucket, ObjectKey: key}
		resource := "arn:aws:s3:::" + bucket + "/" + key
		if purpose == "publish" {
			credentials.ObjectPrefix = prefix + "/"
			resource = "arn:aws:s3:::" + bucket + "/" + credentials.ObjectPrefix + "*"
		}
		return credentials, actions, resource, true
	}
	return biz.TemporaryStorageCredentials{}, nil, "", false
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
