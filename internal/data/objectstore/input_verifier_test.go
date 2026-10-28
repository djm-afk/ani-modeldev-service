package objectstore_test

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

func TestVerifyCSVReadsFixedVersionAndActualRegisteredShape(t *testing.T) {
	// These are module fixture bytes, not the managed production dataset.
	payload := inputCSVFixture(t)
	scope, object := objectFixture(payload)
	object.Key = "tenant/execution/input.csv"
	var gets atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/cpu-artifacts/tenant/execution/input.csv" || r.URL.Query().Get("versionId") != "version-1" {
			t.Errorf("unexpected fixed-input request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("x-amz-version-id", "version-1")
		w.Header().Set("ETag", `"not-the-content-digest"`)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).VerifyCSV(context.Background(), scope, object)
	if err != nil {
		t.Fatalf("verify fixed CSV bytes: %v", err)
	}
	if gets.Load() != 1 || !reflect.DeepEqual(got.Object, object) || got.SchemaVersion != "ani.cpu.csv.v1" || got.RowCount != 1024 || got.FeatureCount != 16 || got.VerifiedAt.IsZero() {
		t.Fatalf("missing observed immutable input facts: %+v", got)
	}
}

func TestVerifyCSVRejectsShapesTheRegisteredTrainerCannotConsume(t *testing.T) {
	valid := inputCSVFixture(t)
	lines := strings.Split(valid, "\r\n")
	cases := []struct {
		name    string
		payload string
	}{
		{"wrong header", strings.Replace(valid, "x0,", "feature0,", 1)},
		{"duplicate header", strings.Replace(valid, "x1,", "x0,", 1)},
		{"BOM header", "\xef\xbb\xbf" + valid},
		{"empty dataset", lines[0] + "\r\n"},
		{"nonbinary label", strings.Replace(valid, ",0\r\n", ",2\r\n", 1)},
		{"fractional label", strings.Replace(valid, ",0\r\n", ",0.0\r\n", 1)},
		{"missing feature", strings.Replace(valid, "0.25,", "", 1)},
		{"empty feature", strings.Replace(valid, "0.25,", ",", 1)},
		{"invalid UTF-8", strings.Replace(valid, "0.25", "\xff", 1)},
		{"NaN", strings.Replace(valid, "0.25", "NaN", 1)},
		{"infinity", strings.Replace(valid, "0.25", "+Inf", 1)},
		{"float32 overflow", strings.Replace(valid, "0.25", "3.5e38", 1)},
		{"malformed quoted field", strings.Replace(valid, "0.25", `0"25`, 1)},
		// Go encoding/csv skips blank lines and ParseFloat accepts hexadecimal
		// floats. The registered Python loader does neither, so these must fail.
		{"blank first row", "\r\n" + valid},
		{"blank trailing row", valid + "\r\n"},
		{"hexadecimal feature", strings.Replace(valid, "0.25", "0x1p-2", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, object := objectFixture(tc.payload)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-amz-version-id", "version-1")
				_, _ = io.WriteString(w, tc.payload)
			}))
			defer server.Close()
			got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).VerifyCSV(context.Background(), scope, object)
			if !errors.Is(err, biz.ErrInputVerification) || !got.VerifiedAt.IsZero() || got.RowCount != 0 {
				t.Fatal("trainer-incompatible bytes produced a verified input observation")
			}
		})
	}
}

func TestVerifyCSVAcceptsActualNonFixedNonemptySampleCounts(t *testing.T) {
	valid := inputCSVFixture(t)
	lines := strings.Split(valid, "\r\n")
	for _, rows := range []int{1, 17, 1023, 1025} {
		t.Run(strconv.Itoa(rows), func(t *testing.T) {
			payload := lines[0] + "\r\n" + strings.Repeat(lines[1]+"\r\n", rows)
			scope, object := objectFixture(payload)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-amz-version-id", "version-1")
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).VerifyCSV(context.Background(), scope, object)
			if err != nil || got.RowCount != int64(rows) || got.FeatureCount != 16 {
				t.Fatalf("actual row count lost: got=%+v err=%v", got, err)
			}
		})
	}
}

func inputCSVFixture(t *testing.T) string {
	t.Helper()
	var buffer strings.Builder
	writer := csv.NewWriter(&buffer)
	writer.UseCRLF = true
	header := make([]string, 17)
	row := make([]string, 17)
	for column := 0; column < 16; column++ {
		header[column] = "x" + strconv.Itoa(column)
		row[column] = "0.25"
	}
	header[16] = "label"
	if err := writer.Write(header); err != nil {
		t.Fatal(err)
	}
	for sample := 0; sample < 1024; sample++ {
		row[16] = strconv.Itoa(sample % 2)
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func TestVerifyCSVAppliesLogicalRecordGuardInsteadOfDatasetOrFieldPolicy(t *testing.T) {
	header := strings.Split(inputCSVFixture(t), "\r\n")[0] + "\r\n"
	tail := "\"" + strings.Repeat(",0", 15) + ",1\r\n"
	for _, extra := range []int{0, 1} {
		row := "\"" + strings.Repeat("0", cpup01.MaxRecordBytes-1-len(tail)+extra) + tail
		payload := header + row
		scope, object := objectFixture(payload)
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("x-amz-version-id", "version-1")
			_, _ = io.WriteString(w, payload)
		}))
		got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).VerifyCSV(context.Background(), scope, object)
		server.Close()
		if extra == 0 {
			if err != nil || got.RowCount != 1 || got.PreparedDataset.SizeBytes != int64(len(payload)) {
				t.Fatalf("exact current logical-record guard rejected: %+v %v", got, err)
			}
		} else {
			var violation *cpup01.LimitViolation
			if !errors.As(err, &violation) || violation.Reason != "DATASET_RECORD_TOO_LARGE" || violation.Actual != int64(cpup01.MaxRecordBytes)+1 || violation.Limit != cpup01.MaxRecordBytes {
				t.Fatalf("record guard lost actual/limit: %v", err)
			}
		}
	}
}
