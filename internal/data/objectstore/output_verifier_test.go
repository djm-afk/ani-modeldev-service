package objectstore_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

func TestCurrentModelTarAcceptsPAXPathsEmptyAncillaryAndSeparateContentLimit(t *testing.T) {
	name := strings.Repeat("long-directory/", 12) + "tokenizer-added.json"
	content := []byte("x")
	digest := func(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
	files := []cpup01.OutputFile{
		{Role: "HF_MODEL", RelativePath: "export/merged/model.safetensors", SizeBytes: 1, SHA256: digest(content), InTar: true},
		{Role: "HF_MODEL", RelativePath: "export/merged/" + name, SizeBytes: 0, SHA256: digest(nil), InTar: true},
	}
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	for i, f := range files {
		if err := writer.WriteHeader(&tar.Header{Name: strings.TrimPrefix(f.RelativePath, "export/merged/"), Mode: 0600, Size: f.SizeBytes, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := writer.Write(content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	contract := cpup01.OutputContract{SchemaVersion: cpup01.OutputContractSchemaVersion, OutputKind: "MERGED_HF_MODEL", CreateTarBundle: true}
	limits := cpup01.ContentLimits{MaxContentBytes: 1}
	if err := objectstore.ValidateFinetuningOutputTar(bytes.NewReader(data.Bytes()), files, contract, limits); err != nil {
		t.Fatalf("valid archive overhead was counted as model content, or actual PAX/empty file refused: %v", err)
	}
	broken := append(append([]byte{}, data.Bytes()...), 1)
	if err := objectstore.ValidateFinetuningOutputTar(bytes.NewReader(broken), files, contract, limits); err == nil {
		t.Fatal("nonzero trailing bytes accepted")
	}
	files[1].InTar = false
	if err := objectstore.ValidateFinetuningOutputTar(bytes.NewReader(data.Bytes()), files, contract, limits); err == nil {
		t.Fatal("undeclared model member accepted")
	}
}
