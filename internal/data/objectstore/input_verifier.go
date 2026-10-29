package objectstore

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// VerifyCSV verifies the exact version's bytes and fixed CPU input structure.
// Import authorization, version fixation and durable READY are separate steps.
func (v *Verifier) VerifyCSV(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef) (biz.VerifiedCSV, error) {
	return v.WritePreparedCSV(ctx, scope, object, nil)
}

// WritePreparedCSV validates and copies the exact source bytes during one GET.
// The caller owns atomic publication after matching the returned frozen facts.
func (v *Verifier) WritePreparedCSV(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, destination io.Writer) (biz.VerifiedCSV, error) {
	if object.SizeBytes <= 0 {
		return biz.VerifiedCSV{}, biz.ErrInputVerification
	}
	if err := ctx.Err(); err != nil {
		return biz.VerifiedCSV{}, err
	}
	if destination == nil {
		destination = io.Discard
	}
	var rows int64
	verified, err := v.verify(ctx, scope, object, func(stream io.Reader) error {
		var err error
		rows, err = inspectCPUCSV(io.TeeReader(stream, destination))
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return biz.VerifiedCSV{}, ctx.Err()
		}
		if errors.Is(err, biz.ErrObjectSourceUnavailable) {
			return biz.VerifiedCSV{}, biz.ErrInputSourceUnavailable
		}
		var limit *cpup01.LimitViolation
		if errors.As(err, &limit) {
			return biz.VerifiedCSV{}, err
		}
		return biz.VerifiedCSV{}, biz.ErrInputVerification
	}
	prepared := cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: verified.Object.SizeBytes, SHA256: verified.Object.SHA256, RowCount: rows}
	return biz.VerifiedCSV{VerifiedObject: verified, SchemaVersion: "ani.cpu.csv.v1", RowCount: rows, FeatureCount: 16, PreparedDataset: prepared}, nil
}

func inspectCPUCSV(stream io.Reader) (int64, error) {
	reader := csv.NewReader(&csvPhysicalLines{reader: &csvBoundedRecords{reader: stream, fieldStart: true}})
	reader.FieldsPerRecord = 17
	reader.ReuseRecord = true
	header, err := reader.Read()
	if err != nil || len(header) != 17 || header[16] != "label" {
		return 0, biz.ErrInputVerification
	}
	for column := 0; column < 16; column++ {
		if header[column] != "x"+strconv.Itoa(column) {
			return 0, biz.ErrInputVerification
		}
	}
	var rows int64
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			var limit *cpup01.LimitViolation
			if errors.As(err, &limit) {
				return 0, limit
			}
			return 0, biz.ErrInputVerification
		}
		if rows == math.MaxInt64 || (row[16] != "0" && row[16] != "1") {
			return 0, biz.ErrInputVerification
		}
		for _, raw := range row[:16] {
			text := strings.TrimSpace(raw)
			if strings.ContainsAny(raw, "\r\n") || !decimalFeaturePattern.MatchString(text) {
				return 0, biz.ErrInputVerification
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
				return 0, biz.ErrInputVerification
			}
		}
		rows++
	}
	if rows == 0 {
		return 0, biz.ErrInputVerification
	}
	return rows, nil
}

// csvBoundedRecords recognizes quoted logical records before encoding/csv may
// allocate their fields. Raw quoting and line terminators count toward the
// single public guard; a physical newline inside quotes is not a new record.
type csvBoundedRecords struct {
	reader                           io.Reader
	size                             int64
	quoted, quotePending, fieldStart bool
}

func (r *csvBoundedRecords) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	for index, value := range buffer[:n] {
		r.size++
		if r.size > cpup01.MaxRecordBytes {
			return index + 1, &cpup01.LimitViolation{Reason: "DATASET_RECORD_TOO_LARGE", Kind: "dataset", LimitName: "MaxRecordBytes", Actual: r.size, Limit: cpup01.MaxRecordBytes}
		}
		if r.quoted {
			if !r.quotePending {
				r.quotePending = value == '"'
				continue
			}
			r.quotePending = false
			if value == '"' {
				continue
			}
			r.quoted = false
		}
		switch value {
		case '\n':
			r.size, r.fieldStart = 0, true
		case ',':
			r.fieldStart = true
		case '"':
			r.quoted = r.fieldStart
			r.fieldStart = false
		case '\r':
		default:
			r.fieldStart = false
		}
	}
	return n, err
}

var decimalFeaturePattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// encoding/csv silently skips blank lines, while the registered Python loader
// counts them as malformed samples. Reject them before that information is lost.
// The fixed input format also excludes multiline numeric cells.
type csvPhysicalLines struct {
	reader  io.Reader
	content bool
}

func (r *csvPhysicalLines) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	for _, value := range p[:n] {
		if value == '\n' {
			if !r.content {
				return n, biz.ErrInputVerification
			}
			r.content = false
		} else if value != '\r' {
			r.content = true
		}
	}
	return n, err
}
