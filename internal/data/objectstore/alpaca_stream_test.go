package objectstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

func TestAlpacaConversionRejectsMalformedOriginalRecords(t *testing.T) {
	for _, source := range []string{
		`[]`, `[{"instruction":"x","input":"","output":"x","output":"y"}]`,
		`[{"instruction":"x","input":"","output":"x","extra":"y"}]`,
		`[{"instruction":"x","input":null,"output":"x"}]`,
		`[{"instruction":"x","input":[],"output":"x"}]`,
		`[{"instruction":" ","input":"","output":"x"}]`,
		`[{"instruction":"x","input":"","output":""}]`,
		`[{"instruction":"x","output":"x"}]`,
		`[{"instruction":"x","input":"","output":"x"},]`,
		`[{"instruction":"x","input":"","output":"x"}] {}`,
		"\xef\xbb\xbf[]", "[{\"instruction\":\"\xff\",\"input\":\"\",\"output\":\"x\"}]",
	} {
		if facts, err := objectstore.ConvertAlpacaJSON(strings.NewReader(source), nil); err == nil || facts.RowCount != 0 {
			t.Fatalf("invalid original JSON was accepted: %q %+v %v", source, facts, err)
		}
	}
}

func TestAlpacaRawRecordGuardAllowsExactAndRejectsOneAdditionalByte(t *testing.T) {
	prefix, suffix := `{"instruction":"`, `","input":"","output":"x"}`
	for _, extra := range []int{0, 1} {
		record := prefix + strings.Repeat("x", cpup01.MaxRecordBytes-len(prefix)-len(suffix)+extra) + suffix
		facts, err := objectstore.ConvertAlpacaJSON(io.MultiReader(strings.NewReader("["), strings.NewReader(record), strings.NewReader("]")), nil)
		if extra == 0 {
			if err != nil || facts.RowCount != 1 || facts.SizeBytes != int64(cpup01.MaxRecordBytes)+1 {
				t.Fatalf("exact structural boundary rejected: %+v %v", facts, err)
			}
		} else {
			var violation *cpup01.LimitViolation
			if !errors.As(err, &violation) || violation.Actual != int64(cpup01.MaxRecordBytes)+1 || violation.Limit != cpup01.MaxRecordBytes || violation.Reason != "DATASET_RECORD_TOO_LARGE" {
				t.Fatalf("record boundary lost structured actual/limit: %v", err)
			}
		}
	}
}

func TestAlpacaNineLongRecordsImportAndPrepareHaveIdenticalFacts(t *testing.T) {
	name := os.Getenv("MODELDEV_TEST_LONG_ALPACA")
	if name == "" {
		t.Skip("explicit Fedora-only 9x30MiB fixture required")
	}
	source, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || info.Size() != 283115530 {
		t.Fatalf("unexpected deterministic fixture: %+v %v", info, err)
	}
	imported, err := objectstore.ConvertAlpacaJSON(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "prepared-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	prepared, err := objectstore.ConvertAlpacaJSON(source, output)
	if err != nil || imported != prepared || prepared.RowCount != 9 || prepared.SizeBytes > info.Size() {
		t.Fatalf("large conversion facts differ: %+v %+v %v", imported, prepared, err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	n, err := io.Copy(digest, output)
	if err != nil || n != prepared.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != prepared.SHA256 {
		t.Fatalf("prepared file bytes differ: n=%d facts=%+v %v", n, prepared, err)
	}
	t.Logf("source_bytes=%d prepared_bytes=%d prepared_sha256=%s rows=%d", info.Size(), prepared.SizeBytes, prepared.SHA256, prepared.RowCount)
}

func TestAlpacaConversionProducesReproducibleCompactJSONL(t *testing.T) {
	source := "[\n { \"output\" : \"ok\", \"input\" : \"\", \"instruction\" : \"<问>\\n\" },\n{\"instruction\":\"two\",\"input\":\"\\u4f60\",\"output\":\"yes\"}\n] \t"
	want := "{\"output\":\"ok\",\"input\":\"\",\"instruction\":\"<问>\\n\"}\n{\"instruction\":\"two\",\"input\":\"\\u4f60\",\"output\":\"yes\"}\n"
	var output bytes.Buffer
	facts, err := objectstore.ConvertAlpacaJSON(strings.NewReader(source), &output)
	sum := sha256.Sum256([]byte(want))
	if err != nil || output.String() != want || facts.Format != "ALPACA_JSONL" || facts.TransformID != "alpaca-array-compact-jsonl-v3" || facts.RowCount != 2 || facts.SizeBytes != int64(len(want)) || facts.SHA256 != hex.EncodeToString(sum[:]) || facts.SizeBytes > int64(len(source)) {
		t.Fatalf("original arrays must derive the exact same bounded JSONL facts: output=%q facts=%+v error=%v", output.String(), facts, err)
	}
	again, err := objectstore.ConvertAlpacaJSON(strings.NewReader(source), nil)
	if err != nil || again != facts {
		t.Fatalf("import hash-only conversion and prepare output conversion differ: %+v %+v %v", facts, again, err)
	}
}

func TestAlpacaImportObservesFrozenSourceAndPreparedFactsInOneGET(t *testing.T) {
	source := "[ {\"instruction\":\"teach\",\"input\":\"\",\"output\":\"answer\"} ]"
	want := "{\"instruction\":\"teach\",\"input\":\"\",\"output\":\"answer\"}\n"
	scope, object := objectFixture(source)
	gets := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		w.Header().Set("x-amz-version-id", "version-1")
		_, _ = io.WriteString(w, source)
	}))
	defer server.Close()
	proof, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).VerifyAlpaca(context.Background(), scope, object)
	sum := sha256.Sum256([]byte(want))
	if err != nil || gets != 1 || proof.Object.SHA256 != object.SHA256 || proof.RowCount != 1 || proof.PreparedDataset.SizeBytes != int64(len(want)) || proof.PreparedDataset.SHA256 != hex.EncodeToString(sum[:]) || proof.PreparedDataset.ValidateForSource("JSON", object.SizeBytes, object.SHA256, proof.RowCount) != nil {
		t.Fatalf("import did not observe both immutable datasets together: proof=%+v gets=%d error=%v", proof, gets, err)
	}
}
