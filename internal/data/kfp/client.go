// Package kfp owns the narrow outbound KFP CreateRun boundary.
package kfp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/client-go/dynamic"
)

var (
	ErrInvalidConfig = errors.New("KFP_INVALID_OWNER_CONFIGURATION")
	ErrNotSent       = errors.New("KFP_CREATE_RUN_NOT_SENT")
	ErrUncertain     = errors.New("KFP_CREATE_RUN_UNCERTAIN")
)

var bearerTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

const maxCreateRunResponseBytes = 1 << 20

// Config is supplied by the owner, never by a browser request. It describes the
// managed connection only; artifact root and all business values come from the
// original frozen dispatch plan, never from current client defaults.
type Config struct {
	ConnectionRef string
	Endpoint      string
	RootCAs       *x509.CertPool
	Timeout       time.Duration
}

// TokenProvider must authenticate the control workload and resolve the trusted
// tenant/binding identity, including TenantProxyIdentity, into a short-lived
// bearer credential. Its implementation must not forward browser headers or
// treat the admission's audit actor as a permanent authorization grant.
type TokenProvider interface {
	BearerToken(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error)
}

// Client is not wired into a product entry point. It creates no durable facts
// or authority. It sends once and only confirms complete matching API responses;
// lost or untrusted responses remain uncertain and never authorize a retry.
type Client struct {
	connectionRef string
	endpoint      string
	tokens        TokenProvider
	http          *http.Client
	identity      dynamic.Interface
}

func New(config Config, tokens TokenProvider) (*Client, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawPath != "" || strings.TrimSpace(config.Endpoint) != config.Endpoint {
		return nil, ErrInvalidConfig
	}
	if endpoint.Path != "" && endpoint.Path != "/" && path.Clean(endpoint.Path) != strings.TrimSuffix(endpoint.Path, "/") {
		return nil, ErrInvalidConfig
	}
	if config.ConnectionRef == "" || strings.TrimSpace(config.ConnectionRef) != config.ConnectionRef || config.RootCAs == nil || config.Timeout <= 0 || config.Timeout > time.Minute || tokens == nil {
		return nil, ErrInvalidConfig
	}
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/apis/v2beta1/runs"
	transport := &http.Transport{
		// Do not inherit an ambient proxy or any caller-supplied retry transport.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: config.Timeout}).DialContext,
		TLSClientConfig:       &tls.Config{RootCAs: config.RootCAs.Clone(), MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   config.Timeout,
		ResponseHeaderTimeout: config.Timeout,
		MaxConnsPerHost:       2,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true,
	}
	return &Client{
		connectionRef: config.ConnectionRef,
		endpoint:      endpoint.String(),
		tokens:        tokens,
		http: &http.Client{
			Transport:     transport,
			Timeout:       config.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (client *Client) CreateRun(ctx context.Context, input biz.PipelineCreateRequest) (biz.PipelineSubmissionObservation, error) {
	notSent := biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionNotSent}
	if client == nil || client.http == nil || client.tokens == nil || ctx == nil || ctx.Err() != nil {
		return notSent, ErrNotSent
	}
	if input.Validate() != nil || input.Plan.Environment.KFPConnectionRef != client.connectionRef {
		return notSent, ErrNotSent
	}
	plan := input.Plan
	environment := plan.Environment
	if client.verifyMutationNamespace(ctx, plan, true) != nil {
		return notSent, ErrNotSent
	}
	token, err := client.tokens.BearerToken(ctx, plan.TenantID, environment)
	if err != nil || len(token) > 16384 || !bearerTokenPattern.MatchString(token) || ctx.Err() != nil {
		return notSent, ErrNotSent
	}
	requestBody, err := json.Marshal(createRunBody{
		ExperimentID: strings.ToLower(environment.ExperimentID),
		DisplayName:  plan.DisplayName,
		PipelineVersionReference: pipelineVersionReference{
			PipelineID:        strings.ToLower(plan.PipelineID),
			PipelineVersionID: strings.ToLower(plan.PipelineVersionID),
		},
		RuntimeConfig: runtimeConfig{
			Parameters:   map[string]string{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash, "environment_binding_id": plan.Environment.BindingID, "namespace_uid": plan.Environment.NamespaceUID},
			PipelineRoot: plan.Owner.PipelineRoot,
		},
		ServiceAccount: environment.Identities.KFPStepServiceAccount,
	})
	if err != nil {
		return notSent, ErrNotSent
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return notSent, ErrNotSent
	}
	// Go's transport must not classify this POST as replayable: never add an
	// Idempotency-Key header and never supply a replay body. DisplayName is not
	// an idempotency key. No caller request/header map is available to forward.
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	uncertain := biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionUncertain}
	response, err := client.http.Do(request)
	if err != nil {
		// Do not leak token-provider errors, request URLs, raw network errors or
		// response bodies. Even a canceled sent request may have created a Run.
		return uncertain, ErrUncertain
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" {
		return uncertain, ErrUncertain
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxCreateRunResponseBytes+1))
	if err != nil || len(responseBody) > maxCreateRunResponseBytes {
		return uncertain, ErrUncertain
	}
	runID := confirmedRunID(responseBody, requestBody)
	if runID == "" {
		return uncertain, ErrUncertain
	}
	return biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: runID}, nil
}

func confirmedRunID(responseBody, requestBody []byte) string {
	if !utf8.Valid(responseBody) {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	if !uniqueJSONValue(decoder, 0) {
		return ""
	}
	var response, request map[string]any
	if json.Unmarshal(responseBody, &response) != nil || json.Unmarshal(requestBody, &request) != nil {
		return ""
	}
	// KFP's converter can return HTTP 200 with run_id plus an error. That is
	// not a complete trustworthy response even when the ID looks well formed.
	if _, hasError := response["error"]; hasError {
		return ""
	}
	for key, wanted := range request {
		// Exact API keys and complete nested values prevent casing aliases,
		// omitted fields or a different execution/version/root from matching.
		if !reflect.DeepEqual(response[key], wanted) {
			return ""
		}
	}
	runID, ok := response["run_id"].(string)
	id, err := uuid.Parse(runID)
	if !ok || err != nil || id == uuid.Nil || id.String() != strings.ToLower(runID) {
		return ""
	}
	// This confirms the observed creation response only. A Run may already
	// report failed computation, and no response grants authoritative ownership.
	return id.String()
}

// encoding/json permits duplicate object keys. Check the bounded response with
// its token API before decoding associations; do not let a later duplicate
// silently replace an earlier Run or nested execution binding.
func uniqueJSONValue(decoder *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, isContainer := token.(json.Delim)
	if !isContainer {
		return true
	}
	switch delimiter {
	case '{':
		keys := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || keys[key] {
				return false
			}
			keys[key] = true
			if !uniqueJSONValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !uniqueJSONValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

// Exact JSON field names follow the fixed KFP 2.16.0 v2beta1 API schema.
type createRunBody struct {
	ExperimentID             string                   `json:"experiment_id"`
	DisplayName              string                   `json:"display_name"`
	PipelineVersionReference pipelineVersionReference `json:"pipeline_version_reference"`
	RuntimeConfig            runtimeConfig            `json:"runtime_config"`
	ServiceAccount           string                   `json:"service_account"`
}

type pipelineVersionReference struct {
	PipelineID        string `json:"pipeline_id"`
	PipelineVersionID string `json:"pipeline_version_id"`
}

type runtimeConfig struct {
	Parameters   map[string]string `json:"parameters"`
	PipelineRoot string            `json:"pipeline_root"`
}

var _ biz.PipelineRunCreator = (*Client)(nil)
