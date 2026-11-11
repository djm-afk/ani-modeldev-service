package biz

import (
	"errors"
	"strings"
	"testing"
)

const storedPreview = `{"schema":"ani.modeldev.dataset.version-preview.v1","total_rows":42,` +
	`"samples":[{"sample_index":1,"instruction":"say hi","input":"","output":"hi","truncated_fields":[]},` +
	`{"sample_index":2,"instruction":"long","input":"x","output":"y","truncated_fields":["instruction"]}],` +
	`"preview_truncated":true}`

func TestDecodeDatasetPreview(t *testing.T) {
	preview, err := DecodeDatasetPreview([]byte(storedPreview))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if preview.TotalRows != 42 || !preview.Truncated || len(preview.Records) != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.MaxRecords != DatasetPreviewMaxRecords || preview.MaxFieldCharacters != DatasetPreviewMaxFieldCharacters {
		t.Fatalf("budgets = %d/%d", preview.MaxRecords, preview.MaxFieldCharacters)
	}
	if preview.Records[1].SampleIndex != 2 || preview.Records[1].TruncatedFields[0] != "instruction" {
		t.Fatalf("record = %+v", preview.Records[1])
	}
}

func TestDecodeDatasetPreviewRejectsUnreadableRows(t *testing.T) {
	base := strings.TrimSuffix(storedPreview, "}")
	for name, raw := range map[string]string{
		"empty":            "",
		"not an object":    `[]`,
		"unknown field":    base + `,"display_limits":{}}`,
		"trailing content": storedPreview + `{}`,
		"wrong schema":     strings.Replace(storedPreview, DatasetPreviewSchema, "ani.modeldev.dataset.version-preview.v2", 1),
		"negative rows":    strings.Replace(storedPreview, `"total_rows":42`, `"total_rows":-1`, 1),
		"too many samples": strings.Replace(storedPreview, `"samples":[`, `"samples":[`+strings.Repeat(`{"sample_index":1,"instruction":"","input":"","output":"","truncated_fields":[]},`, 21), 1),
	} {
		if _, err := DecodeDatasetPreview([]byte(raw)); !errors.Is(err, ErrDatasetPreviewInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}
