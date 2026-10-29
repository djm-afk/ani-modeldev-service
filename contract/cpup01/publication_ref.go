package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

const PublicationCandidateSchemaVersion = "ani.modeldev.publication-candidate.v3"
const PublicationRefSchemaVersion = "ani.modeldev.publication-ref.v3"
const PublicationCandidateRelativePath = "publication/candidate.json"

// PublicationRef is the complete KFP parameter. The role fixes its path; a
// caller can neither choose another file nor inline a publication candidate.
type PublicationRef struct {
	Schema    string `json:"schema"`
	SizeBytes int64  `json:"size_bytes,string"`
	SHA256    string `json:"sha256"`
}
type PublishedFileCandidate struct {
	File   OutputFile     `json:"file"`
	Object FixedObjectRef `json:"object"`
}
type PublicationCandidate struct {
	Schema                string                   `json:"schema"`
	TenantID              string                   `json:"tenant_id"`
	OperationID           string                   `json:"operation_id"`
	ExecutionID           string                   `json:"execution_id"`
	ExecutionSpecHash     string                   `json:"execution_spec_hash"`
	LogicalPublicationKey string                   `json:"logical_publication_key"`
	Files                 []PublishedFileCandidate `json:"files"`
	ManifestObject        FixedObjectRef           `json:"manifest_object"`
	BundleObject          *FixedObjectRef          `json:"bundle_object,omitempty"`
	UploadTaskID          string                   `json:"upload_task_id"`
	UploadPodUID          string                   `json:"upload_pod_uid"`
}

func (r PublicationRef) Canonical() ([]byte, error) {
	if r.Schema != PublicationRefSchemaVersion || r.SizeBytes <= 0 || r.SizeBytes > MaxPublicationCandidateBytes || !snapshotSHA256Pattern.MatchString(r.SHA256) {
		return nil, ErrInvalidArgument
	}
	raw, err := encodeCanonicalJSON(r)
	if len(raw) > MaxPublicationRefBytes {
		return nil, ErrInvalidArgument
	}
	return raw, err
}
func ParsePublicationRef(raw []byte) (PublicationRef, error) {
	var r PublicationRef
	if len(raw) == 0 || len(raw) > MaxPublicationRefBytes || !validUnicodeEscapes(raw) {
		return r, ErrInvalidArgument
	}
	if checkJSONValue(json.NewDecoder(bytes.NewReader(raw)), 0) != nil {
		return r, ErrInvalidArgument
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return r, ErrInvalidArgument
	}
	canonical, err := r.Canonical()
	if err != nil || !bytes.Equal(raw, canonical) {
		return PublicationRef{}, ErrInvalidArgument
	}
	return r, nil
}

func (c PublicationCandidate) Canonical() ([]byte, error) {
	if c.Schema != PublicationCandidateSchemaVersion || !validUUID(c.TenantID) || !validUUID(c.OperationID) || !validUUID(c.ExecutionID) || !snapshotSHA256Pattern.MatchString(c.ExecutionSpecHash) || !validSnapshotText(c.LogicalPublicationKey) || !validSnapshotText(c.UploadTaskID) || !validUUID(c.UploadPodUID) || len(c.Files) == 0 || len(c.Files) > MaxOutputFiles || invalidFixedObject(c.ManifestObject) != "" {
		return nil, ErrInvalidArgument
	}
	if c.ManifestObject.SizeBytes > MaxManifestBytes || c.BundleObject != nil && invalidFixedObject(*c.BundleObject) != "" {
		return nil, ErrInvalidArgument
	}
	seen := map[string]bool{}
	for _, entry := range c.Files {
		if !validSnapshotPath(entry.File.RelativePath) || len(entry.File.RelativePath) > MaxRelativePathBytes || seen[entry.File.RelativePath] || entry.File.SizeBytes < 0 || entry.File.SizeBytes == 0 && entry.File.SHA256 != EmptyContentSHA256 || !snapshotSHA256Pattern.MatchString(entry.File.SHA256) || invalidFixedObjectAllowEmpty(entry.Object) != "" || entry.Object.SizeBytes != entry.File.SizeBytes || entry.Object.SHA256 != entry.File.SHA256 {
			return nil, ErrInvalidArgument
		}
		seen[entry.File.RelativePath] = true
	}
	c.Files = append([]PublishedFileCandidate{}, c.Files...)
	sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].File.RelativePath < c.Files[j].File.RelativePath })
	raw, err := encodeCanonicalJSON(c)
	if len(raw) > MaxPublicationCandidateBytes {
		return nil, ErrInvalidArgument
	}
	return raw, err
}

func ParsePublicationCandidate(raw []byte) (PublicationCandidate, error) {
	var c PublicationCandidate
	if len(raw) == 0 || len(raw) > MaxPublicationCandidateBytes || !validUnicodeEscapes(raw) {
		return c, ErrInvalidArgument
	}
	if checkJSONValue(json.NewDecoder(bytes.NewReader(raw)), 0) != nil {
		return c, ErrInvalidArgument
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, ErrInvalidArgument
	}
	canonical, err := c.Canonical()
	if err != nil || !bytes.Equal(raw, canonical) {
		return PublicationCandidate{}, ErrInvalidArgument
	}
	return c, nil
}

func VerifyPublicationCandidate(ref PublicationRef, raw []byte) (PublicationCandidate, error) {
	if _, err := ref.Canonical(); err != nil || int64(len(raw)) != ref.SizeBytes {
		return PublicationCandidate{}, ErrInvalidArgument
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != ref.SHA256 {
		return PublicationCandidate{}, ErrInvalidArgument
	}
	return ParsePublicationCandidate(raw)
}

func (c PublicationCandidate) ValidateFor(admission AdmissionEnvelope) error {
	if _, _, err := admission.CanonicalPayloads(); err != nil {
		return err
	}
	if _, err := c.Canonical(); err != nil {
		return err
	}
	if c.TenantID != admission.TenantID || c.OperationID != admission.OperationID || c.ExecutionID != admission.ExecutionID || c.ExecutionSpecHash != admission.SpecHash {
		return ErrInvalidArgument
	}
	files := make([]OutputFile, 0, len(c.Files))
	scope := admission.Snapshot.PublicationScope
	objects := []FixedObjectRef{c.ManifestObject}
	if c.BundleObject != nil {
		bound, err := TarReadBound(admission.Snapshot.Limits.MaxContentBytes)
		if err != nil || c.BundleObject.SizeBytes > bound {
			return ErrInvalidArgument
		}
		objects = append(objects, *c.BundleObject)
	}
	for _, file := range c.Files {
		files = append(files, file.File)
		objects = append(objects, file.Object)
	}
	for _, object := range objects {
		if object.StorageConnectionID != scope.StorageConnectionID || object.Bucket != scope.Bucket || !strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") {
			return ErrInvalidArgument
		}
	}
	manifest, digest, err := OutputManifestBytes(admission, files)
	if err != nil {
		return err
	}
	if c.ManifestObject.SHA256 != digest || c.ManifestObject.SizeBytes != int64(len(manifest)) {
		return ErrInvalidArgument
	}
	return nil
}
