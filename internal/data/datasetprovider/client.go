// Package datasetprovider reads bounded dataset repository metadata from the two
// supported public hubs. It never downloads file content: that happens inside the
// importer job, which is the only place that sees bytes.
package datasetprovider

import (
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
	target := strings.TrimSuffix(endpoint, "/") + path
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, biz.ErrSourceInvalidInput
	}
	// Anonymous only. No Authorization, Cookie or user agent claims an account.
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// A transport failure is not a missing source. Reporting it as
		// SOURCE_NOT_FOUND would tell the caller to fix a repository name when
		// the real problem is DNS, egress or the hub being down.
		return 0, biz.ErrSourceUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		// Private or gated repositories are not supported in this release.
		return response.StatusCode, biz.ErrSourceAuthRequired
	case http.StatusNotFound:
		return response.StatusCode, biz.ErrSourceNotFound
	default:
		return response.StatusCode, biz.ErrSourceNotFound
	}
	limited := io.LimitReader(response.Body, MaxMetadataBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return response.StatusCode, biz.ErrSourceNotFound
	}
	if len(raw) > MaxMetadataBytes {
		return response.StatusCode, biz.ErrSourceNotFound
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return response.StatusCode, biz.ErrSourceNotFound
	}
	return response.StatusCode, nil
}
