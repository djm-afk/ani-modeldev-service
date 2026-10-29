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
	errMaterialReplaced     = errors.New("implementation material replaced while reading")
)

const maxMaterialReadAttempts = 3

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
	return readMaterialAtWithRead(ctx, root, implementationID, deviceMode, readMaterialBytes)
}

func readMaterialBytes(file *os.File) ([]byte, error) {
	return io.ReadAll(io.LimitReader(file, cpup01.MaxMaterialBytes+1))
}

func readMaterialAtWithRead(ctx context.Context, root int, implementationID, deviceMode string, read func(*os.File) ([]byte, error)) (cpup01.ImplementationMaterial, error) {
	// Atomic replacement detaches an already-open inode. Reopen only that
	// signal, inside the same directory and caller window, never invalid bytes.
	for attempt := 0; attempt < maxMaterialReadAttempts; attempt++ {
		document, err := readMaterialAttempt(ctx, root, implementationID, deviceMode, read)
		if !errors.Is(err, errMaterialReplaced) {
			return document, err
		}
	}
	if err := ctx.Err(); err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
}

func readMaterialAttempt(ctx context.Context, root int, implementationID, deviceMode string, read func(*os.File) ([]byte, error)) (cpup01.ImplementationMaterial, error) {
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
	return readOpenedMaterial(ctx, file, implementationID, read)
}

func readOpenedMaterial(ctx context.Context, file *os.File, implementationID string, read func(*os.File) ([]byte, error)) (cpup01.ImplementationMaterial, error) {
	fd := int(file.Fd())
	var before syscall.Stat_t
	if syscall.Fstat(fd, &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Size <= 0 || before.Size > cpup01.MaxMaterialBytes {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	if before.Nlink == 0 {
		return cpup01.ImplementationMaterial{}, errMaterialReplaced
	}
	if before.Nlink != 1 {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	if err := ctx.Err(); err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	raw, err := read(file)
	if err := ctx.Err(); err != nil {
		return cpup01.ImplementationMaterial{}, err
	}
	if err != nil {
		return cpup01.ImplementationMaterial{}, ErrCatalogueUnavailable
	}
	var after syscall.Stat_t
	if syscall.Fstat(fd, &after) != nil || int64(len(raw)) != before.Size || after.Size != before.Size || after.Mtim != before.Mtim || after.Mode != before.Mode || after.Uid != before.Uid || after.Gid != before.Gid {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	// Unlinking the original name during rename changes nlink/ctime without
	// changing the opened file's bytes. In-place mutation keeps its rejection.
	if after.Nlink == 0 {
		return cpup01.ImplementationMaterial{}, errMaterialReplaced
	}
	if after.Nlink != 1 || after.Ctim != before.Ctim {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	document, _, err := cpup01.ParseMaterial(raw)
	if err != nil || document.Implementation.ImplementationID != implementationID {
		return cpup01.ImplementationMaterial{}, ErrInvalidMaterial
	}
	return document, nil
}
