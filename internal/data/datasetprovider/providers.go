package datasetprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
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
// then asks for that exact path at the resolved commit. A git blob id is never
// used as a file SHA.
//
// The path lookup runs against the resolved commit, not the requested branch:
// a branch that moves between the two calls would otherwise let the size come
// from a different file than the one the commit pins.
func (c *Client) inspectHuggingFace(ctx context.Context, repoID, revision, filePath string) (biz.SourceFileFact, error) {
	var head huggingFaceRevision
	if _, err := c.getJSON(ctx, c.config.HuggingFaceEndpoint, "/api/datasets/"+repoID+"/revision/"+url.PathEscape(revision), nil, &head); err != nil {
		return biz.SourceFileFact{}, err
	}
	if head.Sha == "" || len(head.Sha) < 7 {
		return biz.SourceFileFact{}, biz.ErrSourceNotPinnable
	}

	file, err := c.huggingFacePathInfo(ctx, repoID, head.Sha, filePath)
	if err != nil {
		return biz.SourceFileFact{}, err
	}
	if file.Type == "directory" {
		// A directory is not a file; never import one because it appeared here.
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

// huggingFacePathInfo asks for one exact path at a resolved commit. It prefers
// the paths-info endpoint, which answers with an array of exactly the requested
// paths, and falls back to the tree endpoint when a mirror does not implement
// paths-info. Both shapes are accepted only when the returned path matches the
// request exactly.
func (c *Client) huggingFacePathInfo(ctx context.Context, repoID, sha, filePath string) (huggingFaceFile, error) {
	var entries []huggingFaceFile
	status, err := c.postJSON(ctx, c.config.HuggingFaceEndpoint,
		"/api/datasets/"+repoID+"/paths-info/"+url.PathEscape(sha),
		map[string]any{"paths": []string{filePath}, "expand": true}, &entries)
	if err == nil {
		if file, ok := exactHuggingFacePath(entries, filePath); ok {
			return file, nil
		}
		return huggingFaceFile{}, biz.ErrSourceNotFound
	}
	// Only a missing endpoint justifies the fallback. A throttled or failing hub
	// must surface as such instead of being retried against another route.
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		return huggingFaceFile{}, err
	}

	// The tree endpoint answers with an array in current versions and with a
	// single object in older ones. Both are read, and both must match exactly.
	var tree huggingFaceEntries
	if _, err := c.getJSON(ctx, c.config.HuggingFaceEndpoint,
		"/api/datasets/"+repoID+"/tree/"+url.PathEscape(sha)+"/"+filePath, nil, &tree); err != nil {
		return huggingFaceFile{}, err
	}
	if file, ok := exactHuggingFacePath(tree, filePath); ok {
		return file, nil
	}
	return huggingFaceFile{}, biz.ErrSourceNotFound
}

// huggingFaceEntries accepts both shapes the tree endpoint has used: an array of
// entries and a single entry object. Decoding only one of them silently turned
// a valid answer into "not found".
type huggingFaceEntries []huggingFaceFile

func (entries *huggingFaceEntries) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var list []huggingFaceFile
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return err
		}
		*entries = list
		return nil
	}
	var single huggingFaceFile
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return err
	}
	*entries = huggingFaceEntries{single}
	return nil
}

// exactHuggingFacePath picks the entry whose path equals the request. A listing
// that returns a parent directory or a sibling is not evidence about this file.
func exactHuggingFacePath(entries []huggingFaceFile, filePath string) (huggingFaceFile, bool) {
	for _, entry := range entries {
		if entry.Path == filePath {
			return entry, true
		}
	}
	return huggingFaceFile{}, false
}

func (c *Client) listHuggingFace(ctx context.Context, repoID, revision, directoryPath, pageToken string, pageSize int32) (biz.SourcePage, error) {
	path := "/api/datasets/" + repoID + "/tree/" + url.PathEscape(revision)
	if directoryPath != "" {
		path += "/" + directoryPath
	}
	query := url.Values{"recursive": {"false"}, "limit": {strconv.FormatInt(int64(pageSize), 10)}}
	if pageToken != "" {
		marker, ok := pageTokenMarker(pageToken, repoID, revision, directoryPath)
		if !ok {
			return biz.SourcePage{}, biz.ErrSourceInvalidInput
		}
		// The hub expects its own cursor, not this adapter's bound token.
		query.Set("cursor", marker)
	}
	var entries []huggingFaceFile
	_, headers, err := c.getJSONWithHeaders(ctx, c.config.HuggingFaceEndpoint, path, query, &entries)
	if err != nil {
		return biz.SourcePage{}, err
	}
	page := biz.SourcePage{}
	for _, entry := range entries {
		page.Entries = append(page.Entries, biz.SourceDirectoryEntry{
			Path: entry.Path, SizeBytes: entry.Size, IsDirectory: entry.Type == "directory",
		})
	}
	// The hub states whether another page exists in its Link header. Inferring
	// "more" from a full page invents a cursor the hub never issued, so the
	// second request would repeat the first page.
	if next := linkNextCursor(headers.Get("Link")); next != "" {
		page.HasMore = true
		page.NextPageToken = makePageToken(repoID, revision, directoryPath, next)
	}
	return page, nil
}

// linkNextCursor extracts the cursor of the rel="next" entry from an RFC 8288
// Link header. An absent or unparsable header means "no next page".
func linkNextCursor(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		target := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		isNext := false
		for _, attribute := range segments[1:] {
			if strings.TrimSpace(attribute) == `rel="next"` {
				isNext = true
				break
			}
		}
		if !isNext {
			continue
		}
		parsed, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"))
		if err != nil {
			return ""
		}
		cursor := parsed.Query().Get("cursor")
		// The token is a pipe-separated tuple, so a cursor containing a pipe
		// cannot be carried inside it. Refusing is safer than minting a token
		// that would be rejected on the next call.
		if strings.Contains(cursor, "|") {
			return ""
		}
		return cursor
	}
	return ""
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
	// Same /repo/tree suffix as the listing: the bare dataset path carries no
	// Files member, which made every INSPECT_FILE answer "not found".
	if _, err := c.getJSON(ctx, c.config.ModelScopeEndpoint, "/api/v1/datasets/"+repoID+"/repo/tree", query, &payload); err != nil {
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
	// The token carries how many matching entries the caller already has. The
	// hub returns the whole tree in one response, so paging is local and the
	// offset is the only thing that makes the second page different from the
	// first.
	offset := 0
	if pageToken != "" {
		marker, ok := pageTokenMarker(pageToken, repoID, revision, directoryPath)
		if !ok {
			return biz.SourcePage{}, biz.ErrSourceInvalidInput
		}
		parsed, err := strconv.Atoi(marker)
		if err != nil || parsed < 0 {
			return biz.SourcePage{}, biz.ErrSourceInvalidInput
		}
		offset = parsed
	}
	var payload modelScopeFile
	// ModelScope needs the /repo/tree suffix for the file listing; the bare
	// dataset path returns repository metadata with no Files member at all, so
	// using it silently produced an empty page instead of an error.
	if _, err := c.getJSON(ctx, c.config.ModelScopeEndpoint, "/api/v1/datasets/"+repoID+"/repo/tree", url.Values{"Revision": {revision}, "Recursive": {"true"}}, &payload); err != nil {
		return biz.SourcePage{}, err
	}
	page := biz.SourcePage{}
	seen := 0
	for _, file := range payload.Data.Files {
		if directoryPath != "" && !strings.HasPrefix(file.Path, directoryPath+"/") {
			continue
		}
		if seen < offset {
			seen++
			continue
		}
		if len(page.Entries) >= int(pageSize) {
			page.HasMore = true
			page.NextPageToken = makePageToken(repoID, revision, directoryPath, strconv.Itoa(offset+len(page.Entries)))
			break
		}
		page.Entries = append(page.Entries, biz.SourceDirectoryEntry{
			Path: file.Path, SizeBytes: file.Size, IsDirectory: file.Type == "tree" || file.Type == "dir",
		})
		seen++
	}
	return page, nil
}

// pageToken is bound to the exact listing identity. A token from another
// repository, revision or directory is rejected rather than followed.
func makePageToken(repoID, revision, directory, marker string) string {
	return repoID + "|" + revision + "|" + directory + "|" + marker
}

// pageTokenMarker returns the marker of a token that belongs to this listing.
func pageTokenMarker(token, repoID, revision, directory string) (string, bool) {
	parts := strings.Split(token, "|")
	if len(parts) != 4 {
		return "", false
	}
	if parts[0] != repoID || parts[1] != revision || parts[2] != directory || parts[3] == "" {
		return "", false
	}
	return parts[3], true
}
