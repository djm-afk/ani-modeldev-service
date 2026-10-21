// Package admissionfacts loads explicitly pinned private admission facts.
// Loading bytes is not evidence that an environment or identity is usable.
package admissionfacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

const (
	schemaVersion = "ani.modeldev.managed-admission-facts.v2.1"
	maxFiles      = 64
	maxFileBytes  = 64 * 1024
	maxJSONDepth  = 16
)

var (
	ErrInvalidFacts     = errors.New("INVALID_MANAGED_ADMISSION_FACTS")
	ErrFactsUnavailable = errors.New("MANAGED_ADMISSION_FACTS_UNAVAILABLE")
)

// FileSource comes only from trusted startup configuration. SHA256 identifies
// the actual file bytes and is neither an ENV binding nor an environment authorization.
type FileSource struct {
	Path   string
	SHA256 string
}

// document is the private file format, not a public RPC or shared contract.
// Evidence references locate independently reviewed source material. Their
// presence and digest shape never establish a successful capability test.
type document struct {
	SchemaVersion       string                            `json:"schema_version"`
	ResourceTenantID    string                            `json:"resource_tenant_id"`
	ImplementationID    string                            `json:"implementation_id"`
	Environment         cpup01.EnvironmentBindingSnapshot `json:"environment"`
	InputScope          cpup01.StorageScope               `json:"input_scope"`
	PublicationScope    cpup01.StorageScope               `json:"publication_scope"`
	Timing              cpup01.TimingConfiguration        `json:"timing"`
	Workspace           cpup01.WorkspaceContract          `json:"workspace"`
	InputFilePath       string                            `json:"input_file_path"`
	OutputDirectoryPath string                            `json:"output_directory_path"`
	EnvironmentEvidence evidenceReference                 `json:"environment_evidence"`
	ApplicationEvidence evidenceReference                 `json:"application_evidence"`
}

type evidenceReference struct {
	Reference string `json:"reference"`
	SHA256    string `json:"sha256"`
}

// Reader owns immutable loaded facts, indexed by tenant and implementation.
// It never approves parameter combinations or performs environment probes.
type Reader struct {
	documents map[documentKey]document
}

type documentKey struct {
	tenantID         string
	implementationID string
}

var _ biz.AdmissionFactsReader = (*Reader)(nil)

func Load(ctx context.Context, sources []FileSource) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(sources) == 0 || len(sources) > maxFiles {
		return nil, ErrInvalidFacts
	}
	documents := make(map[documentKey]document, len(sources))
	var globalTiming *cpup01.TimingConfiguration
	for _, source := range sources {
		value, err := readDocument(ctx, source)
		if err != nil {
			return nil, err
		}
		if globalTiming == nil {
			valueTiming := value.Timing
			globalTiming = &valueTiming
		} else if *globalTiming != value.Timing {
			return nil, ErrInvalidFacts
		}
		key := documentKey{value.ResourceTenantID, value.ImplementationID}
		if _, exists := documents[key]; exists {
			return nil, ErrInvalidFacts
		}
		documents[key] = value
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Reader{documents: documents}, nil
}

func (reader *Reader) ReadAdmissionFacts(ctx context.Context, tenantID, implementationID string) (biz.TenantAdmissionFacts, error) {
	if err := ctx.Err(); err != nil {
		return biz.TenantAdmissionFacts{}, err
	}
	if !canonicalID(tenantID) || (implementationID != "builtin.mlp" && implementationID != "builtin.sft_lora") {
		return biz.TenantAdmissionFacts{}, biz.ErrInvalidAdmission
	}
	if reader == nil {
		return biz.TenantAdmissionFacts{}, biz.ErrAdmissionEnvironmentNotReady
	}
	value, found := reader.documents[documentKey{tenantID, implementationID}]
	if !found {
		return biz.TenantAdmissionFacts{}, biz.ErrAdmissionEnvironmentNotReady
	}
	return biz.TenantAdmissionFacts{TenantID: value.ResourceTenantID, Environment: value.Environment, InputScope: value.InputScope, PublicationScope: value.PublicationScope, Workspace: value.Workspace, InputFilePath: value.InputFilePath, OutputDirectoryPath: value.OutputDirectoryPath, Timing: value.Timing}, nil
}

func readDocument(ctx context.Context, source FileSource) (document, error) {
	if err := ctx.Err(); err != nil {
		return document{}, err
	}
	if !filepath.IsAbs(source.Path) || filepath.Clean(source.Path) != source.Path || strings.ContainsRune(source.Path, 0) || !validDigest(source.SHA256) {
		return document{}, ErrInvalidFacts
	}
	// The pathname is supplied only by trusted startup configuration. Avoid
	// blocking on special files and do not follow a final-component symlink.
	fd, err := syscall.Open(source.Path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return document{}, ErrFactsUnavailable
	}
	file := os.NewFile(uintptr(fd), source.Path)
	defer file.Close()
	var before syscall.Stat_t
	if syscall.Fstat(fd, &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Size <= 0 || before.Size > maxFileBytes {
		return document{}, ErrInvalidFacts
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err := ctx.Err(); err != nil {
		return document{}, err
	}
	if err != nil {
		return document{}, ErrFactsUnavailable
	}
	var after syscall.Stat_t
	if syscall.Fstat(fd, &after) != nil || int64(len(raw)) != before.Size || after.Size != before.Size || after.Nlink != 1 || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return document{}, ErrInvalidFacts
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != source.SHA256 || !utf8.Valid(raw) {
		return document{}, ErrInvalidFacts
	}
	// The token pass rejects duplicate keys, nulls and excessive nesting before
	// the typed decoder enforces the private field set and value types.
	tokens := json.NewDecoder(bytes.NewReader(raw))
	tokens.UseNumber()
	if validJSONValue(tokens, 0) != nil {
		return document{}, ErrInvalidFacts
	}
	if _, err := tokens.Token(); err != io.EOF {
		return document{}, ErrInvalidFacts
	}
	var value document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || !hasRequiredDocumentFields(raw, reflect.TypeOf(value)) || value.SchemaVersion != schemaVersion ||
		!canonicalID(value.ResourceTenantID) || (value.ImplementationID != "builtin.mlp" && value.ImplementationID != "builtin.sft_lora") ||
		!validEvidence(value.EnvironmentEvidence) || !validEvidence(value.ApplicationEvidence) {
		return document{}, ErrInvalidFacts
	}
	if _, err := value.Timing.Resolve(time.Now().UTC().Truncate(time.Microsecond), nil); err != nil {
		return document{}, ErrInvalidFacts
	}
	if err := ctx.Err(); err != nil {
		return document{}, err
	}
	// Compatibility and the complete Snapshot shape remain in the existing biz
	// resolver/shared contract. Loading this document never asserts ENV proof.
	return value, nil
}

// Every field in this private document's tagged structs is required, including
// explicitly empty input credential references. Walk those types instead of
// copying nested field lists or inventing a Snapshot for validation. Scalar and
// slice value types have already been checked by the strict typed decoder.
func hasRequiredDocumentFields(raw []byte, shape reflect.Type) bool {
	if shape.Kind() != reflect.Struct {
		return true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	for index := 0; index < shape.NumField(); index++ {
		field := shape.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return false
		}
		value, present := object[name]
		if !present || !hasRequiredDocumentFields(value, field.Type) {
			return false
		}
	}
	return true
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func validEvidence(value evidenceReference) bool {
	if len(value.Reference) == 0 || len(value.Reference) > 512 || strings.Contains(value.Reference, "://") || !validDigest(value.SHA256) {
		return false
	}
	for _, char := range value.Reference {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:/-", char) {
			continue
		}
		return false
	}
	return true
}

func validJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return ErrInvalidFacts
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return ErrInvalidFacts
	}
	if value, ok := token.(string); ok && strings.ContainsRune(value, utf8.RuneError) {
		// encoding/json otherwise silently replaces invalid UTF-16 escapes.
		return ErrInvalidFacts
	}
	switch token {
	case json.Delim('{'):
		keys := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || keys[key] || key != strings.ToLower(key) || strings.ContainsRune(key, utf8.RuneError) {
				return ErrInvalidFacts
			}
			keys[key] = true
			if err := validJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return ErrInvalidFacts
		}
	case json.Delim('['):
		for decoder.More() {
			if err := validJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return ErrInvalidFacts
		}
	case json.Delim('}'), json.Delim(']'):
		return ErrInvalidFacts
	}
	return nil
}
