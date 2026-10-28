package objectstore

import (
	"context"
	"errors"
	"io"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func (v *Verifier) VerifyAlpaca(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef) (biz.VerifiedAlpaca, error) {
	return v.WritePreparedAlpaca(ctx, scope, object, nil)
}

// WritePreparedAlpaca uses exactly the same conversion as import, during one
// fixed-version read. The destination must remain unpublished until the caller
// matches every returned source/derived fact to its immutable execution.
func (v *Verifier) WritePreparedAlpaca(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, destination io.Writer) (biz.VerifiedAlpaca, error) {
	if object.SizeBytes <= 0 {
		return biz.VerifiedAlpaca{}, biz.ErrInputVerification
	}
	if err := ctx.Err(); err != nil {
		return biz.VerifiedAlpaca{}, err
	}
	var prepared cpup01.PreparedDatasetFacts
	verified, err := v.verify(ctx, scope, object, func(reader io.Reader) error {
		var err error
		prepared, err = ConvertAlpacaJSON(reader, destination)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return biz.VerifiedAlpaca{}, ctx.Err()
		}
		if errors.Is(err, biz.ErrObjectSourceUnavailable) {
			return biz.VerifiedAlpaca{}, biz.ErrInputSourceUnavailable
		}
		var limit *cpup01.LimitViolation
		if errors.As(err, &limit) {
			return biz.VerifiedAlpaca{}, err
		}
		return biz.VerifiedAlpaca{}, biz.ErrInputVerification
	}
	return biz.VerifiedAlpaca{VerifiedObject: verified, SchemaVersion: biz.AlpacaSchemaVersion, RowCount: prepared.RowCount, PreparedDataset: prepared}, nil
}
