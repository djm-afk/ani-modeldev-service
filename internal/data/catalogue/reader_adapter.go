package catalogue

import (
	"context"
	"errors"
	"syscall"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Reader keeps the installed software material directory out of business requests.
type Reader struct{ directory string }

func NewReader(trustedDirectory string) *Reader { return &Reader{directory: trustedDirectory} }
func (reader *Reader) Check(ctx context.Context) error {
	root, err := openCatalogue(ctx, reader.directory)
	if err != nil {
		return err
	}
	if err := syscall.Close(root); err != nil {
		return ErrCatalogueUnavailable
	}
	return ctx.Err()
}
func (reader *Reader) ReadImplementationMaterial(ctx context.Context, implementationID, deviceMode string) (cpup01.ImplementationMaterial, error) {
	document, err := ReadImplementationMaterial(ctx, reader.directory, implementationID, deviceMode)
	switch {
	case err == nil:
		return document, nil
	case errors.Is(err, context.Canceled):
		return cpup01.ImplementationMaterial{}, context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return cpup01.ImplementationMaterial{}, context.DeadlineExceeded
	case errors.Is(err, cpup01.ErrInvalidArgument):
		return cpup01.ImplementationMaterial{}, biz.ErrInvalidAdmission
	case errors.Is(err, ErrMaterialNotFound), errors.Is(err, ErrInvalidMaterial):
		return cpup01.ImplementationMaterial{}, biz.ErrAdmissionEnvironmentNotReady
	default:
		return cpup01.ImplementationMaterial{}, biz.ErrPersistence
	}
}
