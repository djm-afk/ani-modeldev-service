package datasetprovider

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// huggingFaceFile is the metadata shape HF returns for one path. Only the fields
// the design trusts are read; everything else is ignored rather than interpreted.
type huggingFaceFile struct {
	Type string `json:"type"`
	Path string `json:"path"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

type huggingFaceRevision struct {
	Sha string `json:"sha"`
}

// inspectHuggingFace resolves the repo's commit for the requested revision and
// the exact path's size. A git blob id is never used as a file SHA.
func (c *Client) inspectHuggingFace(ctx context.Context, repoID, revision, filePath string) (biz.SourceFileFact, error) {
	var head huggingFaceRevision
	if _, err := c.getJSON(ctx, c.config.HuggingFaceEndpoint, "/api/datasets/"+repoID+"/revision/"+url.PathEscape(revision), nil, &head); err != nil {
		return biz.SourceFileFact{}, err
	}
	if head.Sha == "" || len(head.Sha) < 7 {
		return biz.SourceFileFact{}, biz.ErrSourceNotPinnable
	}

	var file huggingFaceFile
	fileQuery := url.Values{"revision": {revision}, "path": {filePath}, "expand": {"false"}}
	if _, err := c.getJSON(ctx, c.config.HuggingFaceEndpoint, "/api/datasets/"+repoID+"/tree/"+url.PathEscape(revision)+"/"+filePath, fileQuery, &file); err != nil {
		return biz.SourceFileFact{}, err
	}
	if file.Type == "directory" {
		// A directory is not a file; never import one because it appeared here.
		return biz.SourceFileFact{}, biz.ErrSourceNotFound
	}
	// HF tree lookups return arrays in some versions; a single object is the
	// only shape accepted here, so anything else fails closed.
	if file.Path != "" && file.Path != filePath {
		return biz.SourceFileFact{}, biz.ErrSourceNotFound
	}
	size := file.Size
	if size <= 0 && file.LFS != nil {
		size = file.LFS.Size
	}
	if size <= 0 {
		return biz.SourceFileFact{}, biz.ErrSourceSizeUnknown
	}

	fact := biz.SourceFileFact{
		Provider: biz.ProviderHuggingFace, RepoID: repoID, RequestedRevision: revision,
		FilePath: filePath, LockKind: biz.LockImmutableRevision, ResolvedRevision: head.Sha,
		SizeBytes: size,
	}
	// The commit pins the bytes, so a full-file SHA is optional. An LFS pointer
	// hash is NOT the file hash and is deliberately not copied here.
	return fact, nil
}

func (c *Client) listHuggingFace(ctx context.Context, repoID, revision, directoryPath, pageToken string, pageSize int32) (biz.SourcePage, error) {
	path := "/api/datasets/" + repoID + "/tree/" + url.PathEscape(revision)
	if directoryPath != "" {
		path += "/" + directoryPath
	}
	query := url.Values{"recursive": {"false"}, "limit": {strconv.FormatInt(int64(pageSize), 10)}}
	if pageToken != "" {
		if !validPageToken(pageToken, repoID, revision, directoryPath) {
			return biz.SourcePage{}, biz.ErrSourceInvalidInput
		}
		query.Set("cursor", pageToken)
	}
	var entries []huggingFaceFile
	if _, err := c.getJSON(ctx, c.config.HuggingFaceEndpoint, path, query, &entries); err != nil {
		return biz.SourcePage{}, err
	}
	page := biz.SourcePage{}
	for _, entry := range entries {
		page.Entries = append(page.Entries, biz.SourceDirectoryEntry{
			Path: entry.Path, SizeBytes: entry.Size, IsDirectory: entry.Type == "directory",
		})
	}
	if len(entries) == int(pageSize) {
		// A full page means "there may be more"; the token binds identity so a
		// page from another repo/revision/directory cannot be replayed.
		page.HasMore = true
		page.NextPageToken = makePageToken(repoID, revision, directoryPath, strconv.FormatInt(int64(len(entries)), 10))
	}
	return page, nil
}

// modelScopeFile is the metadata shape the ModelScope dataset API returns.
type modelScopeFile struct {
	Data struct {
		Files []struct {
			Path   string `json:"Path"`
			Size   int64  `json:"Size"`
			Sha256 string `json:"Sha256"`
			Type   string `json:"Type"`
		} `json:"Files"`
		Revision string `json:"Revision"`
	} `json:"Data"`
}

// inspectModelScope only claims an immutable revision when the response proves
// one. Otherwise it falls back to a full-file SHA plus size, which the download
// must then match exactly.
func (c *Client) inspectModelScope(ctx context.Context, repoID, revision, filePath string) (biz.SourceFileFact, error) {
	var payload modelScopeFile
	query := url.Values{"Revision": {revision}, "Recursive": {"true"}}
	if _, err := c.getJSON(ctx, c.config.ModelScopeEndpoint, "/api/v1/datasets/"+repoID, query, &payload); err != nil {
		return biz.SourceFileFact{}, err
	}
	for _, file := range payload.Data.Files {
		if file.Path != filePath {
			continue
		}
		if file.Type == "tree" || file.Type == "dir" {
			return biz.SourceFileFact{}, biz.ErrSourceNotFound
		}
		if file.Size <= 0 {
			return biz.SourceFileFact{}, biz.ErrSourceSizeUnknown
		}
		fact := biz.SourceFileFact{
			Provider: biz.ProviderModelScope, RepoID: repoID, RequestedRevision: revision,
			FilePath: filePath, SizeBytes: file.Size,
		}
		// Only adopt a revision lock when the response echoes a concrete commit.
		if payload.Data.Revision != "" && payload.Data.Revision != revision && len(payload.Data.Revision) >= 7 {
			fact.LockKind = biz.LockImmutableRevision
			fact.ResolvedRevision = payload.Data.Revision
			if file.Sha256 != "" {
				fact.FileSHA256 = strings.ToLower(file.Sha256)
			}
			return fact, nil
		}
		// No proven immutable revision: require the full-file SHA instead.
		if file.Sha256 == "" || len(file.Sha256) != 64 {
			return biz.SourceFileFact{}, biz.ErrSourceNotPinnable
		}
		fact.LockKind = biz.LockFileSHA256
		fact.FileSHA256 = strings.ToLower(file.Sha256)
		return fact, nil
	}
	return biz.SourceFileFact{}, biz.ErrSourceNotFound
}

func (c *Client) listModelScope(ctx context.Context, repoID, revision, directoryPath, pageToken string, pageSize int32) (biz.SourcePage, error) {
	var payload modelScopeFile
	if _, err := c.getJSON(ctx, c.config.ModelScopeEndpoint, "/api/v1/datasets/"+repoID, url.Values{"Revision": {revision}, "Recursive": {"true"}}, &payload); err != nil {
		return biz.SourcePage{}, err
	}
	page := biz.SourcePage{}
	for _, file := range payload.Data.Files {
		if directoryPath != "" && !strings.HasPrefix(file.Path, directoryPath+"/") {
			continue
		}
		if len(page.Entries) >= int(pageSize) {
			page.HasMore = true
			page.NextPageToken = makePageToken(repoID, revision, directoryPath, strconv.FormatInt(int64(len(page.Entries)), 10))
			break
		}
		page.Entries = append(page.Entries, biz.SourceDirectoryEntry{
			Path: file.Path, SizeBytes: file.Size, IsDirectory: file.Type == "tree" || file.Type == "dir",
		})
	}
	return page, nil
}

// pageToken is bound to the exact listing identity. A token from another
// repository, revision or directory is rejected rather than followed.
func makePageToken(repoID, revision, directory, marker string) string {
	return repoID + "|" + revision + "|" + directory + "|" + marker
}

func validPageToken(token, repoID, revision, directory string) bool {
	parts := strings.Split(token, "|")
	if len(parts) != 4 {
		return false
	}
	return parts[0] == repoID && parts[1] == revision && parts[2] == directory && parts[3] != ""
}
