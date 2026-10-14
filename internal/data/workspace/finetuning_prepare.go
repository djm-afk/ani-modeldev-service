package workspace

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"golang.org/x/sys/unix"
)

const FinetuningDatasetInfo = `{"ani_cpu_smoke":{"file_name":"finetune_data.json","formatting":"alpaca","columns":{"prompt":"instruction","query":"input","response":"output"}}}`

// PrepareFinetuningInput consumes separate temporary read clients for the
// fixed dataset and fixed Model archive. It keeps source JSON bytes unchanged.
func PrepareFinetuningInput(ctx context.Context, dataClient, baseClient *s3.Client, e biz.Execution, binding biz.WorkspaceBinding, directory string) (biz.WorkspaceBinding, error) {
	if ctx == nil || dataClient == nil || baseClient == nil {
		return biz.WorkspaceBinding{}, biz.ErrInputSourceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if _, _, err := e.CanonicalPayloads(); err != nil || e.Snapshot.Kind != "FINETUNING" || e.Snapshot.Finetuning == nil {
		return biz.WorkspaceBinding{}, biz.ErrInvalidAdmission
	}
	want := e.Snapshot.Workspace
	if binding.Mode != want.Mode || binding.NamespaceName != e.Snapshot.Environment.NamespaceName || binding.NamespaceUID != e.Snapshot.Environment.NamespaceUID || binding.PVCName == "" || binding.PVCUID == "" || binding.InputSubpath != want.InputSubpath || binding.TrainingSubpath != want.TrainingSubpath || binding.ReportsSubpath != want.ReportsSubpath || binding.PublicationSubpath != want.PublicationSubpath {
		return biz.WorkspaceBinding{}, biz.ErrRuntimeConflict
	}
	root, err := openPipelineRoot(directory)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(root)
	if reused, manifest, err := reuseFinetuningPreparation(ctx, root, e, binding); err != nil {
		return biz.WorkspaceBinding{}, err
	} else if reused {
		binding.PreparedManifestSHA256, binding.PreparedManifestBytes = pipelineContentHash(manifest), int64(len(manifest))
		return binding, nil
	}
	input, fine := e.Snapshot.Input, e.Snapshot.Finetuning
	scope := cpup01.StorageScope{StorageConnectionID: input.Object.StorageConnectionID, Bucket: input.Object.Bucket, ApprovedPrefix: path.Dir(input.Object.Key)}
	verified, err := objectstore.NewVerifier(dataClient, input.Object.StorageConnectionID, 32<<20).VerifyAlpaca(ctx, scope, input.Object)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if verified.SchemaVersion != input.SchemaVersion || verified.RowCount != input.RowCount {
		return biz.WorkspaceBinding{}, biz.ErrInputVerification
	}
	data, err := readFixedDataset(ctx, dataClient, input.Object)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	inputs, err := openPipelineSubdirectory(root, e.Snapshot.Workspace.InputSubpath)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(inputs)
	dataDir, err := openPipelineSubdirectory(inputs, "data")
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(dataDir)
	if err := writePipelineFile(ctx, dataDir, "finetune_data.json", data); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if err := writePipelineFile(ctx, dataDir, "dataset_info.json", []byte(FinetuningDatasetInfo)); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	configuration, err := (cpup01.FinetuningConfiguration{ParameterContractVersion: fine.ParameterContractVersion, Parameters: e.Snapshot.Program.ResolvedParameters, ManagedSettings: fine.ManagedSettings}).Canonical()
	if err != nil || pipelineContentHash(configuration) != fine.ResolvedConfigurationSHA256 {
		return biz.WorkspaceBinding{}, biz.ErrInputVerification
	}
	if err := writePipelineFile(ctx, inputs, "finetune_configuration.json", configuration); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	baseManifest, err := fine.BaseModel.BaseManifestBytes()
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if err := prepareBaseArchive(ctx, baseClient, inputs, fine.BaseModel, fine.Budgets); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if err := writePipelineFile(ctx, inputs, "base_manifest.json", baseManifest); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	for _, subpath := range []string{"training/adapter", "export/merged", "reports/train", "reports/export", "reports/verify", e.Snapshot.Workspace.PublicationSubpath + "/spool"} {
		fd, err := openPipelineSubdirectory(root, subpath)
		if err != nil {
			return biz.WorkspaceBinding{}, err
		}
		unix.Close(fd)
	}
	reports, err := openPipelineSubdirectory(root, e.Snapshot.Workspace.ReportsSubpath)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(reports)
	manifest, err := PreparedManifestBytes(e, binding)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if err := writePipelineFile(ctx, reports, "prepared-manifest.json", manifest); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	binding.PreparedManifestSHA256, binding.PreparedManifestBytes = pipelineContentHash(manifest), int64(len(manifest))
	return binding, nil
}

// An existing marker is evidence to check, never permission to repair inputs.
// Reuse requires the actual frozen files and model inventory before returning
// the original candidate; the caller still owns durable Prepared/Task gating.
func reuseFinetuningPreparation(ctx context.Context, root int, e biz.Execution, binding biz.WorkspaceBinding) (bool, []byte, error) {
	reports, err := openPipelineSubdirectory(root, e.Snapshot.Workspace.ReportsSubpath)
	if err != nil {
		return false, nil, err
	}
	defer unix.Close(reports)
	var stat unix.Stat_t
	if err := unix.Fstatat(reports, "prepared-manifest.json", &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return false, nil, nil
	} else if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return false, nil, biz.ErrInputVerification
	}
	fine := e.Snapshot.Finetuning
	configuration, err := (cpup01.FinetuningConfiguration{ParameterContractVersion: fine.ParameterContractVersion, Parameters: e.Snapshot.Program.ResolvedParameters, ManagedSettings: fine.ManagedSettings}).Canonical()
	if err != nil {
		return false, nil, err
	}
	baseManifest, err := fine.BaseModel.BaseManifestBytes()
	if err != nil {
		return false, nil, err
	}
	manifest, err := PreparedManifestBytes(e, binding)
	if err != nil {
		return false, nil, err
	}
	input := e.Snapshot.Workspace.InputSubpath
	files := []cpup01.OutputFile{{RelativePath: input + "/data/finetune_data.json", SizeBytes: e.Snapshot.Input.Object.SizeBytes, SHA256: e.Snapshot.Input.Object.SHA256}}
	for _, file := range []struct {
		name string
		data []byte
	}{
		{input + "/data/dataset_info.json", []byte(FinetuningDatasetInfo)},
		{input + "/finetune_configuration.json", configuration},
		{input + "/base_manifest.json", baseManifest},
		{e.Snapshot.Workspace.ReportsSubpath + "/prepared-manifest.json", manifest},
	} {
		files = append(files, cpup01.OutputFile{RelativePath: file.name, SizeBytes: int64(len(file.data)), SHA256: pipelineContentHash(file.data)})
	}
	for _, expected := range files {
		file, err := capturePipelineFile(ctx, root, expected)
		if err != nil {
			return false, nil, err
		}
		file.Close()
	}
	model, err := openFinetuningFile(root, input+"/model", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return false, nil, err
	}
	defer unix.Close(model)
	if err := verifyBaseTree(ctx, model, fine.BaseModel); err != nil {
		return false, nil, err
	}
	return true, manifest, nil
}

func readFixedDataset(ctx context.Context, client *s3.Client, o cpup01.FixedObjectRef) ([]byte, error) {
	r, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(o.Bucket), Key: aws.String(o.Key), VersionId: o.VersionID})
	if err != nil || r == nil || r.Body == nil {
		return nil, pipelineIOError(ctx, biz.ErrInputSourceUnavailable)
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: r.Body}, o.SizeBytes+1))
	if err != nil {
		return nil, pipelineIOError(ctx, biz.ErrInputSourceUnavailable)
	}
	if r.VersionId == nil || o.VersionID == nil || *r.VersionId != *o.VersionID || int64(len(data)) != o.SizeBytes || pipelineContentHash(data) != o.SHA256 {
		return nil, biz.ErrInputVerification
	}
	return data, nil
}

func prepareBaseArchive(ctx context.Context, client *s3.Client, inputs int, base cpup01.BaseModelRef, budgets cpup01.ComponentBudgets) error {
	if model, err := unix.Openat(inputs, "model", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0); err == nil {
		defer unix.Close(model)
		if err := verifyBaseTree(ctx, model, base); err != nil {
			return err
		}
		// A complete tree without the original completion manifest can also be
		// left by a rejected tar tail or an interrupted prepare. Do not promote
		// those bytes into a successful preparation on a later invocation.
		return biz.ErrInputVerification
	} else if !errors.Is(err, unix.ENOENT) {
		return biz.ErrInputVerification
	}
	o := base.Object
	response, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(o.Bucket), Key: aws.String(o.Key), VersionId: o.VersionID})
	if err != nil || response == nil || response.Body == nil {
		return pipelineIOError(ctx, biz.ErrInputSourceUnavailable)
	}
	defer response.Body.Close()
	if o.VersionID != nil && (response.VersionId == nil || *response.VersionId != *o.VersionID) {
		return biz.ErrInputVerification
	}
	spool, err := openPipelineSubdirectory(inputs, ".spool")
	if err != nil {
		return err
	}
	defer unix.Close(spool)
	var filesystem unix.Statfs_t
	if unix.Fstatfs(spool, &filesystem) != nil || filesystem.Type == unix.TMPFS_MAGIC || filesystem.Type == unix.RAMFS_MAGIC {
		return biz.ErrInvalidWorkspaceOutput
	}
	archive, err := newPipelineTemporaryAt(spool)
	if err != nil {
		return err
	}
	defer archive.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(contextReader{ctx: ctx, reader: response.Body}, o.SizeBytes+1))
	if err != nil {
		return pipelineIOError(ctx, biz.ErrInputSourceUnavailable)
	}
	if size != o.SizeBytes || size > budgets.MaxBaseModelBytes || hex.EncodeToString(hash.Sum(nil)) != o.SHA256 {
		return biz.ErrInputVerification
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return biz.ErrInputVerification
	}
	// This execution's original prepare is the only input writer. A directory
	// is never a readiness signal: failed extraction leaves unready bytes, and
	// a subsequent observation may only verify them, never repair or replace.
	if err := unix.Mkdirat(inputs, "model", 0700); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return biz.ErrInputVerification
		}
		return biz.ErrInvalidWorkspaceOutput
	}
	model, err := unix.Openat(inputs, "model", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	defer unix.Close(model)
	if err := extractBaseModel(ctx, archive, model, base, budgets.MaxBaseModelExpandedBytes); err != nil {
		return err
	}
	if err := verifyBaseTree(ctx, model, base); err != nil {
		return err
	}
	if err := unix.Fsync(model); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	return unix.Fsync(inputs)
}

func verifyBaseTree(ctx context.Context, root int, base cpup01.BaseModelRef) error {
	expected := map[string]cpup01.BaseModelFile{}
	directories := map[string]bool{}
	for _, file := range base.Files {
		expected[file.Path] = file
		for dir := path.Dir(file.Path); dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
		}
	}
	seen := map[string]bool{}
	var visit func(int, string) error
	visit = func(parent int, prefix string) error {
		dup, err := unix.Openat(parent, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		directory := os.NewFile(uintptr(dup), prefix)
		defer directory.Close()
		names, err := directory.Readdirnames(-1)
		if err != nil {
			return err
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			relative := name
			if prefix != "" {
				relative = prefix + "/" + name
			}
			if directories[relative] {
				fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return biz.ErrInputVerification
				}
				err = visit(fd, relative)
				unix.Close(fd)
				if err != nil {
					return err
				}
				continue
			}
			want, ok := expected[relative]
			if !ok || seen[relative] {
				return biz.ErrInputVerification
			}
			fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return biz.ErrInputVerification
			}
			valid, err := hashBaseMember(ctx, fd, want)
			unix.Close(fd)
			if err != nil || !valid {
				return biz.ErrInputVerification
			}
			seen[relative] = true
		}
		return nil
	}
	if err := visit(root, ""); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return biz.ErrInputVerification
	}
	return nil
}

func extractBaseModel(ctx context.Context, archive io.Reader, root int, base cpup01.BaseModelRef, limit int64) error {
	expected := make(map[string]cpup01.BaseModelFile, len(base.Files))
	for _, file := range base.Files {
		expected[file.Path] = file
	}
	seen := make(map[string]bool, len(expected))
	reader := tar.NewReader(contextReader{ctx: ctx, reader: archive})
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return biz.ErrInputVerification
		}
		if header.Format != tar.FormatUSTAR || header.Typeflag != tar.TypeReg || !biz.ValidStorageKey(header.Name) || strings.Contains(header.Name, "\\") || seen[header.Name] || len(header.PAXRecords) != 0 || header.Linkname != "" {
			return biz.ErrInputVerification
		}
		want, ok := expected[header.Name]
		if !ok || header.Size != want.SizeBytes || header.Size <= 0 || header.Size > limit {
			return biz.ErrInputVerification
		}
		seen[header.Name] = true
		limit -= header.Size
		var parent int
		if path.Dir(header.Name) == "." {
			parent, err = unix.Dup(root)
		} else {
			parent, err = openPipelineSubdirectory(root, path.Dir(header.Name))
		}
		if err != nil {
			return err
		}
		fd, err := unix.Openat(parent, path.Base(header.Name), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		unix.Close(parent)
		if err != nil {
			return biz.ErrInputVerification
		}
		file := os.NewFile(uintptr(fd), header.Name)
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(file, hash), reader)
		syncErr := file.Sync()
		modeErr := file.Chmod(0444)
		closeErr := file.Close()
		if copyErr != nil || syncErr != nil || modeErr != nil || closeErr != nil || n != want.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
			return pipelineIOError(ctx, biz.ErrInputVerification)
		}
	}
	if len(seen) != len(expected) {
		return biz.ErrInputVerification
	}
	// tar.Reader accepts normal zero padding, but arbitrary appended data is
	// outside the approved immutable archive even after the last member.
	buffer := make([]byte, 32<<10)
	for {
		n, err := archive.Read(buffer)
		for _, b := range buffer[:n] {
			if b != 0 {
				return biz.ErrInputVerification
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return biz.ErrInputVerification
		}
	}
	return nil
}

func openFinetuningFile(root int, relative string, flags int, mode uint32) (int, error) {
	if !biz.ValidStorageKey(relative) || strings.Contains(relative, "\\") {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	current, err := unix.Dup(root)
	if err != nil {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	unix.CloseOnExec(current)
	parts := strings.Split(relative, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(current)
		if err != nil {
			return -1, biz.ErrInvalidWorkspaceOutput
		}
		current = next
	}
	fd, err := unix.Openat(current, parts[len(parts)-1], flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	unix.Close(current)
	if err != nil {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	return fd, nil
}

func hashBaseMember(ctx context.Context, fd int, want cpup01.BaseModelFile) (bool, error) {
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size != want.SizeBytes {
		return false, biz.ErrInputVerification
	}
	// The caller closes fd; do not wrap ownership in os.File and close twice.
	dup, err := unix.Dup(fd)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(dup), want.Path)
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx: ctx, reader: file}, want.SizeBytes+1))
	var after unix.Stat_t
	return err == nil && n == want.SizeBytes && hex.EncodeToString(h.Sum(nil)) == want.SHA256 && unix.Fstat(fd, &after) == nil && after.Nlink == 1 && before.Size == after.Size && before.Mtim == after.Mtim && before.Ctim == after.Ctim, err
}
