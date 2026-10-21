package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func (v *Verifier) VerifyAlpaca(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef) (biz.VerifiedAlpaca, error) {
	if err := ctx.Err(); err != nil {
		return biz.VerifiedAlpaca{}, err
	}
	if object.SizeBytes <= 0 || object.SizeBytes > 32*1024*1024 {
		return biz.VerifiedAlpaca{}, biz.ErrInputVerification
	}
	var rows uint32
	verified, err := v.verify(ctx, scope, object, func(r io.Reader) error {
		var err error
		rows, err = inspectAlpacaJSON(r)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return biz.VerifiedAlpaca{}, ctx.Err()
		}
		if errors.Is(err, biz.ErrObjectSourceUnavailable) {
			return biz.VerifiedAlpaca{}, biz.ErrInputSourceUnavailable
		}
		return biz.VerifiedAlpaca{}, biz.ErrInputVerification
	}
	return biz.VerifiedAlpaca{VerifiedObject: verified, SchemaVersion: biz.AlpacaSchemaVersion, RowCount: rows}, nil
}

func inspectAlpacaJSON(stream io.Reader) (uint32, error) {
	// This allocation is restricted to the 32 MiB dataset contract. Model
	// archives and output weights use the independent streaming I/O adapters.
	raw, err := io.ReadAll(io.LimitReader(stream, 32*1024*1024+1))
	if err != nil || len(raw) > 32*1024*1024 || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return 0, biz.ErrInputVerification
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return 0, biz.ErrInputVerification
	}
	var rows uint32
	for d.More() {
		if token, err := d.Token(); err != nil || token != json.Delim('{') {
			return 0, biz.ErrInputVerification
		}
		seen := make(map[string]bool, 3)
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] || (key != "instruction" && key != "input" && key != "output") {
				return 0, biz.ErrInputVerification
			}
			seen[key] = true
			token, err = d.Token()
			value, ok := token.(string)
			if err != nil || !ok || (key != "input" && strings.TrimSpace(value) == "") {
				return 0, biz.ErrInputVerification
			}
		}
		if token, err := d.Token(); err != nil || token != json.Delim('}') || len(seen) != 3 {
			return 0, biz.ErrInputVerification
		}
		rows++
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') || rows == 0 {
		return 0, biz.ErrInputVerification
	}
	if _, err := d.Token(); err != io.EOF {
		return 0, biz.ErrInputVerification
	}
	return rows, nil
}
