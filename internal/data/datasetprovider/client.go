// Package datasetprovider reads bounded dataset repository metadata from the two
// supported public hubs. It never downloads file content: that happens inside the
// importer job, which is the only place that sees bytes.
package datasetprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

const (
	// Metadata responses are control messages, not data files. Anything larger
	// is a provider anomaly, not a big dataset.
	MaxMetadataBytes = 4 << 20
	// One listing page is bounded so a single inspection cannot walk a repo.
	MaxPageSize     = 100
	DefaultPageSize = 20
	requestTimeout  = 10 * time.Second
)

// Config pins the two hub endpoints. They are deployment values, never taken
// from a request.
type Config struct {
	HuggingFaceEndpoint string
	ModelScopeEndpoint  string
	Timeout             time.Duration
}

// Client implements biz.SourceProviderPort over plain HTTP.
type Client struct {
	config Config
	http   *http.Client
}

// New builds the client. Anonymous access only: no token, cookie or ambient
// credential is ever attached, and redirects are not followed so a provider
// cannot silently move the request to another host.
func New(config Config) (*Client, error) {
	if config.HuggingFaceEndpoint == "" || config.ModelScopeEndpoint == "" {
		return nil, errors.New("dataset provider endpoints are required")
	}
	for _, endpoint := range []string{config.HuggingFaceEndpoint, config.ModelScopeEndpoint} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" {
			return nil, errors.New("dataset provider endpoint must be an https origin")
		}
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = requestTimeout
	}
	return &Client{config: config, http: &http.Client{
		Timeout: timeout,
		// A provider redirect could point at a private address; refuse instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// UseHTTPClient replaces the transport client. Only tests use it, to trust a
// local TLS server; production construction always builds its own client with
// the fixed redirect and timeout rules.
func (c *Client) UseHTTPClient(client *http.Client) {
	if c != nil && client != nil {
		c.http = client
	}
}

var _ biz.SourceProviderPort = (*Client)(nil)

// InspectFile resolves one exact path. The result is either an immutable
// revision (with an optional full-file SHA) or a full-file SHA plus size; the
// design forbids accepting anything weaker than one of those.
func (c *Client) InspectFile(ctx context.Context, provider biz.SourceProvider, repoID, revision, filePath string) (biz.SourceFileFact, error) {
	if c == nil || !biz.ValidSourceProvider(provider) {
		return biz.SourceFileFact{}, biz.ErrSourceInvalidInput
	}
	if repoID == "" || revision == "" {
		return biz.SourceFileFact{}, biz.ErrSourceInvalidInput
	}
	if err := biz.ValidateSourceFilePath(filePath); err != nil {
		return biz.SourceFileFact{}, err
	}
	switch provider {
	case biz.ProviderHuggingFace:
		return c.inspectHuggingFace(ctx, repoID, revision, filePath)
	case biz.ProviderModelScope:
		return c.inspectModelScope(ctx, repoID, revision, filePath)
	}
	return biz.SourceFileFact{}, biz.ErrSourceInvalidInput
}

// ListDirectory returns one bounded page. The token is opaque and is verified to
// be bound to provider/repo/revision/directory before use.
func (c *Client) ListDirectory(ctx context.Context, provider biz.SourceProvider, repoID, revision, directoryPath, pageToken string, pageSize int32) (biz.SourcePage, error) {
	if c == nil || !biz.ValidSourceProvider(provider) || repoID == "" || revision == "" {
		return biz.SourcePage{}, biz.ErrSourceInvalidInput
	}
	if directoryPath != "" && strings.HasPrefix(directoryPath, "/") || strings.Contains(directoryPath, "\\") {
		return biz.SourcePage{}, biz.ErrSourceInvalidInput
	}
	size := pageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		return biz.SourcePage{}, biz.ErrSourceInvalidInput
	}
	switch provider {
	case biz.ProviderHuggingFace:
		return c.listHuggingFace(ctx, repoID, revision, directoryPath, pageToken, size)
	case biz.ProviderModelScope:
		return c.listModelScope(ctx, repoID, revision, directoryPath, pageToken, size)
	}
	return biz.SourcePage{}, biz.ErrSourceInvalidInput
}

// getJSON performs one bounded anonymous GET and decodes into out.
func (c *Client) getJSON(ctx context.Context, endpoint, path string, query url.Values, out any) (int, error) {
	status, _, err := c.doJSON(ctx, http.MethodGet, endpoint, path, query, nil, out)
	return status, err
}

// getJSONWithHeaders is getJSON plus the response headers, for endpoints whose
// pagination cursor lives in a Link header rather than in the body.
func (c *Client) getJSONWithHeaders(ctx context.Context, endpoint, path string, query url.Values, out any) (int, http.Header, error) {
	return c.doJSON(ctx, http.MethodGet, endpoint, path, query, nil, out)
}

// postJSON performs one bounded anonymous POST with a JSON body. Hugging Face
// exposes exact-path metadata only through paths-info, which is a POST.
func (c *Client) postJSON(ctx context.Context, endpoint, path string, body any, out any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, biz.ErrSourceInvalidInput
	}
	status, _, err := c.doJSON(ctx, http.MethodPost, endpoint, path, nil, encoded, out)
	return status, err
}

// doJSON performs one bounded anonymous request and decodes into out. The
// returned status is the upstream one, so a caller can distinguish "this
// endpoint does not exist" from "this file does not exist".
func (c *Client) doJSON(ctx context.Context, method, endpoint, path string, query url.Values, body []byte, out any) (int, http.Header, error) {
	target := strings.TrimSuffix(endpoint, "/") + path
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, biz.ErrSourceInvalidInput
	}
	// Anonymous only. No Authorization, Cookie or user agent claims an account.
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		// A transport failure is not a missing source. Reporting it as
		// SOURCE_NOT_FOUND would tell the caller to fix a repository name when
		// the real problem is DNS, egress or the hub being down.
		return 0, nil, biz.ErrSourceUnavailable
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return response.StatusCode, response.Header, err
	}
	limited := io.LimitReader(response.Body, MaxMetadataBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		// A truncated or reset metadata body is a hub problem, not a missing
		// file: the repository answered 200 and then failed mid-response.
		return response.StatusCode, response.Header, biz.ErrSourceUnavailable
	}
	if len(raw) > MaxMetadataBytes {
		// The hub answered with more metadata than any real listing needs. That
		// is an anomaly on its side, so it must not be reported as "not found".
		return response.StatusCode, response.Header, biz.ErrSourceUnavailable
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return response.StatusCode, response.Header, biz.ErrSourceUnavailable
	}
	return response.StatusCode, response.Header, nil
}

// statusError maps an upstream status onto the source vocabulary. Rate limiting
// and hub outages are retryable; only a real 404 means the file is absent.
func statusError(status int) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		// Private or gated repositories are not supported in this release.
		return biz.ErrSourceAuthRequired
	case status == http.StatusNotFound:
		return biz.ErrSourceNotFound
	case status == http.StatusTooManyRequests:
		// The hub is throttling this caller. The repository and file may be
		// perfectly valid, so this must stay retryable.
		return biz.ErrSourceUnavailable
	case status >= 500:
		return biz.ErrSourceUnavailable
	case status >= 400:
		// Any other 4xx is a request this adapter built wrongly, not a missing
		// file and not a transient outage.
		return biz.ErrSourceInvalidInput
	default:
		return biz.ErrSourceUnavailable
	}
}
