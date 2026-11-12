package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrSourceNotFound     = errors.New("SOURCE_NOT_FOUND")
	ErrSourceAuthRequired = errors.New("SOURCE_AUTH_REQUIRED")
	ErrSourceNotPinnable  = errors.New("SOURCE_NOT_PINNABLE")
	ErrSourceSizeUnknown  = errors.New("SOURCE_SIZE_UNKNOWN")
	ErrSourceChanged      = errors.New("SOURCE_CHANGED")
	ErrSourceFileInvalid  = errors.New("SOURCE_FILE_INVALID")
	ErrSourceInvalidInput = errors.New("SOURCE_INVALID_INPUT")
	// ErrSourceTooLarge means the confirmed file exceeds the limit this
	// deployment accepts. It is a content verdict, not a hub problem.
	ErrSourceTooLarge = errors.New("SOURCE_TOO_LARGE")
	// ErrSourceUnavailable is a transport-level failure reaching the hub (DNS,
	// egress, TLS, hub outage). It is deliberately distinct from
	// SOURCE_NOT_FOUND: the repository may well exist, and the caller must not
	// be told to fix its name.
	ErrSourceUnavailable = errors.New("IMPORT_DEPENDENCY_UNAVAILABLE")
)

// SourceProvider is the two supported public hubs. Both are dataset repos only.
type SourceProvider string

const (
	ProviderHuggingFace SourceProvider = "HUGGINGFACE"
	ProviderModelScope  SourceProvider = "MODELSCOPE"
)

func ValidSourceProvider(value SourceProvider) bool {
	return value == ProviderHuggingFace || value == ProviderModelScope
}

// SourceLockKind is how a task stays pinned to the bytes a user confirmed.
type SourceLockKind string

const (
	LockImmutableRevision SourceLockKind = "IMMUTABLE_REVISION"
	LockFileSHA256        SourceLockKind = "FILE_SHA256"
)

const (
	SourceSelectionSchema = "ani.modeldev.dataset.source-selection.v1"
	SourceSnapshotSchema  = "ani.modeldev.dataset.source-snapshot.v1"
)

// SourceSelection is the user's choice after the server resolved it. It carries
// no resolved_at and no digest, so the same confirmed file always fingerprints
// the same way.
type SourceSelection struct {
	Provider           SourceProvider
	RepoID             string
	RequestedRevision  string
	FilePath           string
	LockKind           SourceLockKind
	ResolvedRevision   string
	ExpectedSizeBytes  int64
	ExpectedFileSHA256 string
}

// SourceSnapshot is a SourceSelection plus the moment it was resolved.
type SourceSnapshot struct {
	SourceSelection
	ResolvedAt time.Time
}

// ValidateFile rejects paths that a repository lookup could interpret two
// different ways. The provider adapter uses the exact path; nothing here guesses
// from a basename.
func ValidateSourceFilePath(path string) error {
	if path == "" || len(path) > 1024 {
		return ErrSourceInvalidInput
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || strings.ContainsRune(path, 0) {
		return ErrSourceInvalidInput
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrSourceInvalidInput
		}
	}
	// The public Alpaca corpora ship as either one JSON array (.json) or one
	// object per line (.jsonl). Both are the same records, and the converter
	// derives the identical normalised JSONL from either, so both are accepted
	// here. Any other extension is refused: this feature does not sniff formats.
	if !strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".jsonl") {
		return ErrSourceInvalidInput
	}
	return nil
}

// Validate checks the frozen lock facts. A task cannot be accepted without a
// positive size and a lock that actually pins bytes.
func (snapshot SourceSnapshot) Validate() error {
	if !ValidSourceProvider(snapshot.Provider) || snapshot.RepoID == "" || len(snapshot.RepoID) > 256 {
		return ErrSourceInvalidInput
	}
	if snapshot.RequestedRevision == "" {
		return ErrSourceInvalidInput
	}
	if err := ValidateSourceFilePath(snapshot.FilePath); err != nil {
		return err
	}
	if snapshot.ExpectedSizeBytes <= 0 {
		return ErrSourceSizeUnknown
	}
	switch snapshot.LockKind {
	case LockImmutableRevision:
		if snapshot.ResolvedRevision == "" {
			return ErrSourceNotPinnable
		}
	case LockFileSHA256:
		if !isLowerSHA256(snapshot.ExpectedFileSHA256) {
			return ErrSourceNotPinnable
		}
	default:
		return ErrSourceNotPinnable
	}
	if snapshot.ExpectedFileSHA256 != "" && !isLowerSHA256(snapshot.ExpectedFileSHA256) {
		return ErrSourceNotPinnable
	}
	if snapshot.ResolvedAt.IsZero() || snapshot.ResolvedAt.Nanosecond()%1000 != 0 {
		return ErrSourceInvalidInput
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// SelectionFingerprint is the digest the client echoes back on submit. It
// covers the nine identity fields and the schema, nothing else.
func (selection SourceSelection) SelectionFingerprint() string {
	fields := []string{
		"provider=" + string(selection.Provider),
		"repo_id=" + selection.RepoID,
		"requested_revision=" + selection.RequestedRevision,
		"file_path=" + selection.FilePath,
		"lock_kind=" + string(selection.LockKind),
		"resolved_revision=" + selection.ResolvedRevision,
		"expected_size_bytes=" + strconv.FormatInt(selection.ExpectedSizeBytes, 10),
		"expected_file_sha256=" + selection.ExpectedFileSHA256,
	}
	return digestFields(SourceSelectionSchema, fields)
}

// SnapshotSHA256 covers the same identity plus resolved_at.
func (snapshot SourceSnapshot) SnapshotSHA256() string {
	fields := []string{
		"provider=" + string(snapshot.Provider),
		"repo_id=" + snapshot.RepoID,
		"requested_revision=" + snapshot.RequestedRevision,
		"file_path=" + snapshot.FilePath,
		"lock_kind=" + string(snapshot.LockKind),
		"resolved_revision=" + snapshot.ResolvedRevision,
		"expected_size_bytes=" + strconv.FormatInt(snapshot.ExpectedSizeBytes, 10),
		"expected_file_sha256=" + snapshot.ExpectedFileSHA256,
		"resolved_at=" + snapshot.ResolvedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
	}
	return digestFields(SourceSnapshotSchema, fields)
}

// CanonicalJSON renders the snapshot as the fixed field set the database CHECK
// compares against the typed columns, keys in lexical order.
func (snapshot SourceSnapshot) CanonicalJSON() []byte {
	fields := [][2]string{
		{"expected_file_sha256", snapshot.ExpectedFileSHA256},
		{"expected_size_bytes", strconv.FormatInt(snapshot.ExpectedSizeBytes, 10)},
		{"file_path", snapshot.FilePath},
		{"lock_kind", string(snapshot.LockKind)},
		{"provider", string(snapshot.Provider)},
		{"repo_id", snapshot.RepoID},
		{"repo_type", "dataset"},
		{"requested_revision", snapshot.RequestedRevision},
		{"resolved_at", snapshot.ResolvedAt.UTC().Format("2006-01-02T15:04:05.000000Z")},
		{"resolved_revision", snapshot.ResolvedRevision},
		{"schema", SourceSnapshotSchema},
	}
	var builder strings.Builder
	builder.WriteByte('{')
	for index, field := range fields {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`"` + field[0] + `":`)
		if field[0] == "expected_size_bytes" {
			builder.WriteString(`"` + field[1] + `"`)
			continue
		}
		builder.WriteString(`"` + field[1] + `"`)
	}
	builder.WriteByte('}')
	return []byte(builder.String())
}

func digestFields(schema string, fields []string) string {
	sum := sha256.Sum256([]byte(schema + "\x00" + strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// SourceFileFact is what a provider adapter reports for one exact path. It is a
// metadata observation, never a download.
type SourceFileFact struct {
	Provider          SourceProvider
	RepoID            string
	RequestedRevision string
	FilePath          string
	LockKind          SourceLockKind
	ResolvedRevision  string
	SizeBytes         int64
	FileSHA256        string
	Directories       []string
}

// SourceDirectoryEntry is one item of a bounded file listing page.
type SourceDirectoryEntry struct {
	Path        string
	SizeBytes   int64
	IsDirectory bool
}

// SourcePage is one page of a repository listing. page_token is opaque and
// bound to provider/repo/revision/directory.
type SourcePage struct {
	Entries       []SourceDirectoryEntry
	NextPageToken string
	HasMore       bool
}

// SourceProviderPort is the outbound hub boundary. It only does bounded
// metadata; downloading happens inside the importer job.
type SourceProviderPort interface {
	// InspectFile resolves one exact path to frozen facts.
	InspectFile(ctx context.Context, provider SourceProvider, repoID, revision, filePath string) (SourceFileFact, error)
	// ListDirectory returns a bounded page.
	ListDirectory(ctx context.Context, provider SourceProvider, repoID, revision, directoryPath, pageToken string, pageSize int32) (SourcePage, error)
}
