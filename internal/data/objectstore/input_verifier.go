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
	if err := ctx.Err(); err != nil {
		return biz.VerifiedCSV{}, err
	}
	if object.SizeBytes <= 0 || object.SizeBytes > 32*1024*1024 {
		return biz.VerifiedCSV{}, biz.ErrInputVerification
	}
	var rows uint32
	verified, err := v.verify(ctx, scope, object, func(stream io.Reader) error {
		var err error
		rows, err = inspectCPUCSV(stream)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return biz.VerifiedCSV{}, ctx.Err()
		}
		if errors.Is(err, biz.ErrObjectSourceUnavailable) {
			return biz.VerifiedCSV{}, biz.ErrInputSourceUnavailable
		}
		return biz.VerifiedCSV{}, biz.ErrInputVerification
	}
	return biz.VerifiedCSV{VerifiedObject: verified, SchemaVersion: "ani.cpu.csv.v1", RowCount: rows, FeatureCount: 16}, nil
}

func inspectCPUCSV(stream io.Reader) (uint32, error) {
	reader := csv.NewReader(&csvPhysicalLines{reader: stream})
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
	var rows uint32
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || rows == math.MaxUint32 || (row[16] != "0" && row[16] != "1") {
			return 0, biz.ErrInputVerification
		}
		for _, raw := range row[:16] {
			// The registered CPython CSV loader limits each decoded field to
			// 128 Ki characters. A byte bound is conservative for UTF-8 and
			// prevents a verified decimal from failing that loader's parsing.
			if len(raw) > 128*1024 {
				return 0, biz.ErrInputVerification
			}
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
