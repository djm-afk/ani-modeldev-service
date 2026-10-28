package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
)

const DatasetManifestSchemaVersion = "ani.modeldev.dataset-manifest.v3"

// PreparedDatasetFacts records independently reproduced bytes, not a new
// mutable input format or a second admission policy.
type PreparedDatasetFacts struct {
	Format      string `json:"format"`
	TransformID string `json:"transform_id"`
	SizeBytes   int64  `json:"size_bytes,string"`
	SHA256      string `json:"sha256"`
	RowCount    int64  `json:"row_count,string"`
}

func (facts PreparedDatasetFacts) ValidateForSource(format string, size int64, checksum string, rows int64) error {
	if size <= 0 || rows <= 0 || facts.RowCount != rows || facts.SizeBytes <= 0 || facts.SizeBytes > size || !snapshotSHA256Pattern.MatchString(checksum) || !snapshotSHA256Pattern.MatchString(facts.SHA256) {
		return ErrInvalidArgument
	}
	switch format {
	case "CSV":
		if facts.Format != "CPU_CSV" || facts.TransformID != "csv-identity-v3" || facts.SizeBytes != size || facts.SHA256 != checksum {
			return ErrInvalidArgument
		}
	case "JSON":
		if facts.Format != "ALPACA_JSONL" || facts.TransformID != "alpaca-array-compact-jsonl-v3" {
			return ErrInvalidArgument
		}
	default:
		return ErrInvalidArgument
	}
	return nil
}

type DatasetManifest struct {
	SchemaVersion        string `json:"schema_version"`
	InputVersionID       string `json:"input_version_id"`
	SourceFormat         string `json:"source_format"`
	SourceSizeBytes      int64  `json:"source_size_bytes,string"`
	SourceSHA256         string `json:"source_sha256"`
	RowCount             int64  `json:"row_count,string"`
	PreparedFormat       string `json:"prepared_format"`
	TransformID          string `json:"transform_id"`
	PreparedRelativePath string `json:"prepared_relative_path"`
	PreparedSizeBytes    int64  `json:"prepared_size_bytes,string"`
	PreparedSHA256       string `json:"prepared_sha256"`
}

func BuildDatasetManifest(input InputRef) (DatasetManifest, error) {
	if !validUUID(input.InputVersionID) || input.PreparedDataset.ValidateForSource(input.Format, input.Object.SizeBytes, input.Object.SHA256, input.RowCount) != nil {
		return DatasetManifest{}, ErrInvalidArgument
	}
	path := "inputs/data.csv"
	if input.PreparedDataset.Format == "ALPACA_JSONL" {
		path = "inputs/data/finetune_data.jsonl"
	}
	return DatasetManifest{SchemaVersion: DatasetManifestSchemaVersion, InputVersionID: input.InputVersionID, SourceFormat: input.Format, SourceSizeBytes: input.Object.SizeBytes, SourceSHA256: input.Object.SHA256, RowCount: input.RowCount, PreparedFormat: input.PreparedDataset.Format, TransformID: input.PreparedDataset.TransformID, PreparedRelativePath: path, PreparedSizeBytes: input.PreparedDataset.SizeBytes, PreparedSHA256: input.PreparedDataset.SHA256}, nil
}

func (manifest DatasetManifest) Canonical() ([]byte, error) {
	input := InputRef{InputVersionID: manifest.InputVersionID, Format: manifest.SourceFormat, Object: FixedObjectRef{SizeBytes: manifest.SourceSizeBytes, SHA256: manifest.SourceSHA256}, RowCount: manifest.RowCount, PreparedDataset: PreparedDatasetFacts{Format: manifest.PreparedFormat, TransformID: manifest.TransformID, SizeBytes: manifest.PreparedSizeBytes, SHA256: manifest.PreparedSHA256, RowCount: manifest.RowCount}}
	expected, err := BuildDatasetManifest(input)
	if err != nil || manifest != expected {
		return nil, ErrInvalidArgument
	}
	return encodeCanonicalJSON(manifest)
}

func (manifest DatasetManifest) Digest() (string, error) {
	raw, err := manifest.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
