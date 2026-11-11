package biz

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// DatasetPreview is the stored, already-bounded preview of one published
// version. The importer wrote it once at publication time under the same
// budgets the reader enforces, so reading it back never re-reads the object.
type DatasetPreview struct {
	InputVersionID     string
	TotalRows          int64
	Records            []DatasetPreviewRecord
	Truncated          bool
	MaxRecords         int32
	MaxFieldCharacters int32
}

// DatasetPreviewRecord is one display record. TruncatedFields names the fields
// the display budget shortened, so the UI can mark them instead of guessing.
type DatasetPreviewRecord struct {
	SampleIndex     int64
	Instruction     string
	Input           string
	Output          string
	TruncatedFields []string
}

// Preview budgets from design §11.1. They are reported to the caller so the UI
// can explain why a record looks short.
const (
	DatasetPreviewMaxRecords         int32 = 20
	DatasetPreviewMaxFieldCharacters int32 = 2000
)

// DatasetPreviewSchema is the only stored preview shape this service reads.
const DatasetPreviewSchema = "ani.modeldev.dataset.version-preview.v1"

var ErrDatasetPreviewInvalid = errors.New("DATASET_PREVIEW_INVALID")

// DecodeDatasetPreview parses the stored preview object. Unknown fields are
// rejected: the row is written by this service, so a shape it does not know is
// corruption rather than forward compatibility.
func DecodeDatasetPreview(raw []byte) (DatasetPreview, error) {
	preview := DatasetPreview{
		MaxRecords:         DatasetPreviewMaxRecords,
		MaxFieldCharacters: DatasetPreviewMaxFieldCharacters,
	}
	// The version row stores a non-null JSON object, so an empty value is a
	// missing preview rather than an empty one. Reporting zero rows would claim
	// the version has no data.
	if len(raw) == 0 {
		return DatasetPreview{}, ErrDatasetPreviewInvalid
	}
	var stored struct {
		Schema           string `json:"schema"`
		TotalRows        int64  `json:"total_rows"`
		PreviewTruncated bool   `json:"preview_truncated"`
		Samples          []struct {
			SampleIndex     int64    `json:"sample_index"`
			Instruction     string   `json:"instruction"`
			Input           string   `json:"input"`
			Output          string   `json:"output"`
			TruncatedFields []string `json:"truncated_fields"`
		} `json:"samples"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return DatasetPreview{}, ErrDatasetPreviewInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return DatasetPreview{}, ErrDatasetPreviewInvalid
	}
	if stored.Schema != DatasetPreviewSchema || stored.TotalRows < 0 || len(stored.Samples) > int(DatasetPreviewMaxRecords) {
		return DatasetPreview{}, ErrDatasetPreviewInvalid
	}
	preview.TotalRows = stored.TotalRows
	preview.Truncated = stored.PreviewTruncated
	preview.Records = make([]DatasetPreviewRecord, 0, len(stored.Samples))
	for _, sample := range stored.Samples {
		preview.Records = append(preview.Records, DatasetPreviewRecord{
			SampleIndex: sample.SampleIndex, Instruction: sample.Instruction,
			Input: sample.Input, Output: sample.Output, TruncatedFields: sample.TruncatedFields,
		})
	}
	return preview, nil
}
