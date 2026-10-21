// Package catalogue reads the finite installed implementation software materials.
package catalogue

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var (
	ErrMaterialNotFound     = errors.New("IMPLEMENTATION_MATERIAL_NOT_FOUND")
	ErrInvalidMaterial      = errors.New("INVALID_IMPLEMENTATION_MATERIAL")
	ErrCatalogueUnavailable = errors.New("CATALOGUE_UNAVAILABLE")
)

// ReadImplementationMaterial selects only the finite implementation/device filename.
// It does not import, approve, enable or bind parameter/compute combinations.
func ReadImplementationMaterial(ctx context.Context, directory, implementationID, deviceMode string) (cpup01.ImplementationMaterial, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	if (implementationID != "builtin.mlp" && implementationID != "builtin.sft_lora") || (deviceMode != "cpu" && deviceMode != "gpu") {
		return cpup01.ImplementationMaterial{}, cpup01.ErrInvalidArgument
	}
	root, err := openCatalogue(ctx, directory)
	if err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	defer syscall.Close(root)
	return readMaterialAt(ctx, root, implementationID, deviceMode)
}

// openCatalogue applies the same directory boundary to startup and each read.
// It neither enumerates materials nor retains a descriptor between requests.
func openCatalogue(ctx context.Context, directory string) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return -1, cpup01.ErrInvalidArgument
	}
	root, err := syscall.Open(directory, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrCatalogueUnavailable
	}
	return root, nil
}

// readMaterialAt keeps reads inside the original trusted directory descriptor.
func readMaterialAt(ctx context.Context, root int, implementationID, deviceMode string) (cpup01.ImplementationMaterial, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	name := implementationID + "." + deviceMode + ".json"
	fd, err := syscall.Openat(root, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return cpup01.ImplementationMaterial{}, ErrMaterialNotFound
		}
		return cpup01.ImplementationMaterial{}, ErrCatalogueUnavailable
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before syscall.Stat_t
	if syscall.Fstat(fd, &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Size <= 0 || before.Size > cpup01.MaxMaterialBytes {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	if err := ctx.Err(); err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, cpup01.MaxMaterialBytes+1))
	if err := ctx.Err(); err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	if err != nil {
		return cpup01.ImplementationMaterial{}, ErrCatalogueUnavailable
	}
	var after syscall.Stat_t
	if syscall.Fstat(fd, &after) != nil || int64(len(raw)) != before.Size || after.Size != before.Size || after.Nlink != 1 || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	document, _, err := cpup01.ParseMaterial(raw)
	if err != nil || document.Implementation.ImplementationID != implementationID {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	return document, nil
}
