// Package workspace implements access to a managed execution workspace.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Collect reads the registered CPU outputs under a trusted mounted training
// directory. Its caller owns authentication, workspace UID binding and writer
// closure; this adapter does not accept user-selected host paths.
func Collect(ctx context.Context, directory string, execution biz.Execution) (biz.CollectedOutput, error) {
	if err := ctx.Err(); err != nil {
		return biz.CollectedOutput{}, err
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return biz.CollectedOutput{}, biz.ErrInvalidWorkspaceOutput
	}
	// The registered CPU contract contains only four fixed leaf names. Opening
	// each leaf relative to this descriptor prevents traversal and directory
	// replacement from redirecting the read after the root has been opened.
	root, err := openPipelineRoot(directory)
	if err != nil {
		return biz.CollectedOutput{}, biz.ErrInvalidWorkspaceOutput
	}
	defer syscall.Close(root)
	if execution.Snapshot.Kind == "FINETUNING" {
		return collectFinetuning(ctx, root, execution)
	}
	contract := execution.Snapshot.OutputContract
	files := make([]biz.CollectedFile, 0, len(contract.RequiredFiles))
	remaining := execution.Snapshot.Limits.MaxContentBytes
	for _, required := range contract.RequiredFiles {
		if err := ctx.Err(); err != nil {
			return biz.CollectedOutput{}, err
		}
		limit := remaining
		entry, err := collectFile(ctx, root, required.RelativePath, required.Role, limit)
		if err != nil {
			return biz.CollectedOutput{}, err
		}
		entry.InTar = required.InTar
		files = append(files, entry)
		remaining -= entry.SizeBytes
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	if err := verifyMLPReports(ctx, root, execution, files); err != nil {
		return biz.CollectedOutput{}, err
	}
	manifest, manifestHash, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(execution.Admission), files)
	if err != nil {
		return biz.CollectedOutput{}, biz.ErrInvalidWorkspaceOutput
	}
	return biz.CollectedOutput{
		ExecutionID: execution.ExecutionID, ExecutionSpecHash: execution.SpecHash,
		StorageState: "WORKSPACE_ONLY", Files: files,
		Manifest: manifest, ManifestSHA256: manifestHash,
	}, nil
}

func collectFile(ctx context.Context, root int, name, role string, limit int64) (biz.CollectedFile, error) {
	if limit < 0 {
		return biz.CollectedFile{}, biz.ErrInvalidWorkspaceOutput
	}
	// NONBLOCK prevents an untrusted FIFO replacement from blocking before the
	// descriptor's type is checked. No symlink or multiply-linked file is read.
	fd, err := openPipelineReadFile(root, name)
	if err != nil {
		return biz.CollectedFile{}, biz.ErrInvalidWorkspaceOutput
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before syscall.Stat_t
	if syscall.Fstat(fd, &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > limit {
		return biz.CollectedFile{}, biz.ErrInvalidWorkspaceOutput
	}
	digest := sha256.New()
	reader := contextReader{ctx: ctx, reader: file}
	size, err := io.Copy(digest, io.LimitReader(reader, limit))
	if err != nil {
		if ctx.Err() != nil {
			return biz.CollectedFile{}, ctx.Err()
		}
		return biz.CollectedFile{}, biz.ErrInvalidWorkspaceOutput
	}
	var extra [1]byte
	n, readErr := reader.Read(extra[:])
	if ctx.Err() != nil {
		return biz.CollectedFile{}, ctx.Err()
	}
	var after syscall.Stat_t
	if n != 0 || readErr != io.EOF || syscall.Fstat(fd, &after) != nil || size != before.Size || after.Size != before.Size || after.Nlink != 1 || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return biz.CollectedFile{}, biz.ErrInvalidWorkspaceOutput
	}
	return biz.CollectedFile{RelativePath: name, Role: role, SizeBytes: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
