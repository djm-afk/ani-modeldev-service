package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"golang.org/x/sys/unix"
)

// PrepareInput is called by the registered prepare component on its own mounted
// execution PVC. The directory is component configuration, never an RPC field.
func PrepareInput(ctx context.Context, client *s3.Client, execution biz.Execution, binding biz.WorkspaceBinding, directory string) (biz.WorkspaceBinding, error) {
	if ctx == nil || client == nil {
		return biz.WorkspaceBinding{}, biz.ErrInputSourceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return biz.WorkspaceBinding{}, biz.ErrInvalidAdmission
	}
	want := execution.Snapshot.Workspace
	if binding.Mode != want.Mode || binding.NamespaceName != execution.Snapshot.Environment.NamespaceName || binding.NamespaceUID != execution.Snapshot.Environment.NamespaceUID ||
		binding.PVCName == "" || binding.PVCUID == "" || binding.InputSubpath != want.InputSubpath || binding.TrainingSubpath != want.TrainingSubpath ||
		binding.ReportsSubpath != want.ReportsSubpath || binding.PublicationSubpath != want.PublicationSubpath || binding.WorkSubpath != want.WorkSubpath {
		return biz.WorkspaceBinding{}, biz.ErrRuntimeConflict
	}
	root, err := openPipelineRoot(directory)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(root)
	if err := prepareWorkDirectories(ctx, root, want.WorkSubpath); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	manifest, err := PreparedManifestBytes(execution, binding)
	if err != nil {
		return biz.WorkspaceBinding{}, biz.ErrInputVerification
	}
	inputs, err := openPipelineSubdirectory(root, want.InputSubpath)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(inputs)
	reports, err := openPipelineSubdirectory(root, want.ReportsSubpath)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(reports)
	if err := prepareFrozenDataset(ctx, client, inputs, execution.Snapshot.Input); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if err := writePipelineFile(ctx, reports, "prepared-manifest.json", manifest); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	binding.PreparedManifestSHA256, binding.PreparedManifestBytes = pipelineContentHash(manifest), int64(len(manifest))
	return binding, nil
}

// PreparedManifestBytes is the shared byte contract for the prepare component
// and the owner's independent verification. Encoding alone proves no I/O or
// permission to use the claimed workspace.
func PreparedManifestBytes(execution biz.Execution, binding biz.WorkspaceBinding) ([]byte, error) {
	input := execution.Snapshot.Input
	dataset, err := cpup01.BuildDatasetManifest(input)
	if err != nil {
		return nil, err
	}
	datasetHash, err := dataset.Digest()
	if err != nil {
		return nil, err
	}
	var baseArchive, baseFiles, datasetInfo, configuration *string
	if fine := execution.Snapshot.Finetuning; fine != nil {
		base, err := fine.BaseModel.BaseManifestSHA256()
		if err != nil {
			return nil, err
		}
		archive := fine.BaseModel.Object.SHA256
		info := pipelineContentHash([]byte(FinetuningDatasetInfo))
		config := fine.ResolvedConfigurationSHA256
		baseArchive, baseFiles, datasetInfo, configuration = &archive, &base, &info, &config
	}
	return json.Marshal(struct {
		Schema                      string  `json:"schema"`
		TenantID                    string  `json:"tenant_id"`
		OperationID                 string  `json:"operation_id"`
		ExecutionID                 string  `json:"execution_id"`
		SpecHash                    string  `json:"execution_spec_hash"`
		InputVersionID              string  `json:"input_version_id"`
		InputSHA256                 string  `json:"input_sha256"`
		InputBytes                  int64   `json:"input_size_bytes,string"`
		DatasetManifestSHA256       string  `json:"dataset_manifest_sha256"`
		NamespaceUID                string  `json:"namespace_uid"`
		PVCUID                      string  `json:"pvc_uid"`
		BaseModelArchiveSHA256      *string `json:"base_model_archive_sha256,omitempty"`
		BaseManifestSHA256          *string `json:"base_manifest_sha256,omitempty"`
		DatasetInfoSHA256           *string `json:"dataset_info_sha256,omitempty"`
		ResolvedConfigurationSHA256 *string `json:"resolved_configuration_sha256,omitempty"`
	}{"ani.modeldev.prepared-manifest.v3", execution.TenantID, execution.OperationID, execution.ExecutionID, execution.SpecHash,
		input.InputVersionID, input.Object.SHA256, input.Object.SizeBytes, datasetHash, binding.NamespaceUID, binding.PVCUID, baseArchive, baseFiles, datasetInfo, configuration})
}

// UploadOutput sends independently collected actual files and the manifest.
// This returns a candidate; only a later step may ask the owner to verify and
// publish after the uploader container has actually terminated successfully.
func UploadOutput(ctx context.Context, client *s3.Client, execution biz.Execution, directory string, output biz.CollectedOutput) (biz.RuntimePublication, error) {
	if ctx == nil || client == nil {
		return biz.RuntimePublication{}, publicationFailure("validate", "client", biz.ErrObjectSourceUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return biz.RuntimePublication{}, publicationFailure("validate", "context", err)
	}
	manifest, manifestSHA, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(execution.Admission), output.Files)
	if err != nil || output.ExecutionID != execution.ExecutionID || output.ExecutionSpecHash != execution.SpecHash || output.StorageState != "WORKSPACE_ONLY" ||
		!bytes.Equal(output.Manifest, manifest) || output.ManifestSHA256 != manifestSHA {
		return biz.RuntimePublication{}, publicationFailure("validate", "manifest", biz.ErrInvalidWorkspaceOutput)
	}
	scope := execution.Snapshot.PublicationScope
	if !biz.ValidStorageKey(scope.ApprovedPrefix) {
		return biz.RuntimePublication{}, publicationFailure("validate", "scope", biz.ErrInvalidWorkspaceOutput)
	}
	root, err := openPipelineRoot(directory)
	if err != nil {
		return biz.RuntimePublication{}, publicationFailure("open", "directory", err)
	}
	defer unix.Close(root)
	files := append([]cpup01.OutputFile{}, output.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	spool := -1
	limits := execution.Snapshot.Limits
	prefix := scope.ApprovedPrefix + "/" + execution.ExecutionID + "/"
	keys := []string{prefix + "output-manifest.json"}
	if execution.Snapshot.OutputContract.CreateTarBundle {
		keys = append(keys, prefix+"bundle.tar")
	}
	for _, file := range files {
		keys = append(keys, prefix+file.RelativePath)
	}
	for _, key := range keys {
		if !biz.ValidStorageKey(key) || len(key) > cpup01.MaxStorageKeyBytes {
			return biz.RuntimePublication{}, publicationFailure("validate", "storage-key", &cpup01.LimitViolation{Reason: "STRUCTURE_LIMIT_EXCEEDED", Kind: "storage_key", LimitName: "MaxStorageKeyBytes", Actual: int64(len(key)), Limit: cpup01.MaxStorageKeyBytes})
		}
	}
	if execution.Snapshot.Kind == "FINETUNING" {
		spool, err = openPipelineSubdirectory(root, execution.Snapshot.Workspace.PublicationSubpath+"/spool")
		if err != nil {
			return biz.RuntimePublication{}, publicationFailure("spool", "directory", err)
		}
		defer unix.Close(spool)
		var filesystem unix.Statfs_t
		if unix.Fstatfs(spool, &filesystem) != nil || filesystem.Type == unix.TMPFS_MAGIC {
			return biz.RuntimePublication{}, publicationFailure("spool", "filesystem", biz.ErrInvalidWorkspaceOutput)
		}
		var payload int64
		for _, file := range files {
			payload, err = cpup01.CheckedAdd(payload, file.SizeBytes)
			if err != nil {
				return biz.RuntimePublication{}, publicationFailure("spool", "overflow", err)
			}
		}
		tarBytes, err := modelTarSize(files)
		if err != nil {
			return biz.RuntimePublication{}, publicationFailure("spool", "tar-size", err)
		}
		required, err := cpup01.CheckedAdd(payload, tarBytes)
		if err == nil {
			required, err = cpup01.CheckedAdd(required, int64(len(manifest))+cpup01.MaxPublicationCandidateBytes)
		}
		free, freeErr := cpup01.CheckedMultiply(int64(filesystem.Bavail), filesystem.Bsize)
		if err != nil || freeErr != nil || free < required {
			return biz.RuntimePublication{}, publicationFailure("spool", "disk-space", biz.ErrInvalidWorkspaceOutput)
		}
	}
	// Capture and validate all inputs before emitting any upload. Unlinked
	// temporary files bound memory use and keep later source mutation out of PUT.
	captured := make([]*os.File, 0, len(files))
	defer func() {
		for _, file := range captured {
			_ = file.Close()
		}
	}()
	for _, file := range files {
		capture, err := capturePipelineFileInto(ctx, root, file, spool)
		if err != nil {
			return biz.RuntimePublication{}, publicationFailure("capture", "content", err)
		}
		captured = append(captured, capture)
	}
	var bundle *os.File
	var bundleBytes int64
	var bundleHash string
	if execution.Snapshot.OutputContract.CreateTarBundle {
		if execution.Snapshot.Kind == "FINETUNING" {
			bundle, bundleBytes, bundleHash, err = bundleFinetuningFiles(ctx, execution.Snapshot.OutputContract, files, captured, spool, limits)
		} else {
			bundle, bundleBytes, bundleHash, err = bundlePipelineFiles(ctx, files, captured, manifest)
		}
		if err != nil {
			return biz.RuntimePublication{}, publicationFailure("bundle", "archive", err)
		}
		defer bundle.Close()
	}
	if execution.Snapshot.Kind == "FINETUNING" {
		if err := verifyFinetuningInventory(ctx, root, execution.Snapshot.OutputContract); err != nil {
			return biz.RuntimePublication{}, publicationFailure("inventory", "content", err)
		}
	}
	candidate := biz.RuntimePublication{LogicalKey: "cpu-p01:" + execution.ExecutionID}
	for i, file := range files {
		object, err := uploadPipelineObject(ctx, client, scope, prefix+file.RelativePath, file.SizeBytes, file.SHA256, captured[i], "put")
		if err != nil {
			return biz.RuntimePublication{}, err
		}
		candidate.Files = append(candidate.Files, biz.PublishedRuntimeFile{File: file, Object: object})
	}
	candidate.Manifest, err = uploadPipelineObject(ctx, client, scope, prefix+"output-manifest.json", int64(len(manifest)), manifestSHA, bytes.NewReader(manifest), "put-manifest")
	if err != nil {
		return biz.RuntimePublication{}, err
	}
	if bundle != nil {
		object, err := uploadPipelineObject(ctx, client, scope, prefix+"bundle.tar", bundleBytes, bundleHash, bundle, "put-bundle")
		if err != nil {
			return biz.RuntimePublication{}, err
		}
		candidate.Bundle = &object
	}
	return candidate, nil
}

func openPipelineRoot(directory string) (int, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	for _, component := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		next, err := unix.Openat(root, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(root)
		if err != nil {
			return -1, biz.ErrInvalidWorkspaceOutput
		}
		root = next
	}
	return root, nil
}

func openPipelineSubdirectory(root int, subpath string) (int, error) {
	if !biz.ValidStorageKey(subpath) {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	current, err := unix.Dup(root)
	if err != nil {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	unix.CloseOnExec(current)
	for _, component := range strings.Split(subpath, "/") {
		if err := unix.Mkdirat(current, component, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			_ = unix.Close(current)
			return -1, biz.ErrInvalidWorkspaceOutput
		}
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(current)
		if err != nil {
			return -1, biz.ErrInvalidWorkspaceOutput
		}
		current = next
	}
	return current, nil
}

func writePipelineFile(ctx context.Context, root int, name string, content []byte) error {
	temporary := ".cpu-p01-" + uuid.NewString()
	fd, err := unix.Openat(root, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer file.Close()
	defer unix.Unlinkat(root, temporary, 0)
	if _, err := io.Copy(file, contextReader{ctx: ctx, reader: bytes.NewReader(content)}); err != nil {
		return pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
	}
	if err := file.Sync(); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Linkat atomically creates a previously absent name on the same mounted
	// filesystem, including CephFS where RENAME_NOREPLACE is not supported.
	// It cannot replace an existing target. Remove our temporary name before
	// verification so the published file must have exactly one remaining link.
	if err := unix.Linkat(root, temporary, root, name, 0); err != nil && !errors.Is(err, unix.EEXIST) {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := unix.Unlinkat(root, temporary, 0); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	// Both new publication and retries require the actual bytes and inode
	// constraints. A crash leaving two names remains untrusted; never remove
	// an arbitrary alias or overwrite a conflicting file to make a retry pass.
	existing, err := capturePipelineFile(ctx, root, cpup01.OutputFile{RelativePath: name, SizeBytes: int64(len(content)), SHA256: pipelineContentHash(content)})
	if err != nil {
		return err
	}
	_ = existing.Close()
	if err := unix.Fsync(root); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	return nil
}

func capturePipelineFile(ctx context.Context, root int, expected cpup01.OutputFile) (*os.File, error) {
	return capturePipelineFileInto(ctx, root, expected, -1)
}

func capturePipelineFileInto(ctx context.Context, root int, expected cpup01.OutputFile, spool int) (*os.File, error) {
	if expected.SizeBytes < 0 || !biz.ValidStorageKey(expected.RelativePath) {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	fd, err := openPipelineReadFile(root, expected.RelativePath)
	if err != nil {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	source := os.NewFile(uintptr(fd), expected.RelativePath)
	defer source.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size != expected.SizeBytes {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	staged, err := newPipelineTemporaryAt(spool)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = staged.Close()
		}
	}()
	digest := sha256.New()
	reader := contextReader{ctx: ctx, reader: source}
	size, err := io.Copy(io.MultiWriter(staged, digest), io.LimitReader(reader, expected.SizeBytes))
	if err != nil {
		return nil, pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
	}
	var extra [1]byte
	n, readErr := reader.Read(extra[:])
	var after unix.Stat_t
	if n != 0 || readErr != io.EOF || size != expected.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != expected.SHA256 ||
		unix.Fstat(fd, &after) != nil || after.Mode&unix.S_IFMT != unix.S_IFREG || after.Size != before.Size || after.Nlink != 1 || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return nil, pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
	}
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	ok = true
	return staged, nil
}

func newPipelineTemporary() (*os.File, error) {
	file, err := os.CreateTemp("", "cpu-p01-bytes-*")
	if err != nil {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	// Linux keeps the descriptor alive after unlink. The component's configured
	// TMPDIR owns the temporary storage; no mutable pathname remains to replace.
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	return file, nil
}

func newPipelineTemporaryAt(spool int) (*os.File, error) {
	if spool < 0 {
		return newPipelineTemporary()
	}
	name := ".finetuning-bytes-" + uuid.NewString()
	fd, err := unix.Openat(spool, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	file := os.NewFile(uintptr(fd), name)
	if unix.Unlinkat(spool, name, 0) != nil {
		file.Close()
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	return file, nil
}

func bundleFinetuningFiles(ctx context.Context, contract cpup01.OutputContract, files []cpup01.OutputFile, captured []*os.File, spool int, limits cpup01.ContentLimits) (*os.File, int64, string, error) {
	if limits.Validate() != nil || len(files) != len(captured) {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	if existing, size, hash, found, err := originalFinetuningBundle(ctx, spool, contract, files, limits); found || err != nil {
		return existing, size, hash, err
	}
	inTar := map[string]bool{}
	for _, file := range files {
		if file.InTar {
			inTar[file.RelativePath] = true
		}
	}
	size, err := modelTarSize(files)
	maximum, boundErr := cpup01.TarReadBound(limits.MaxContentBytes)
	if err != nil || boundErr != nil || len(inTar) == 0 || size > maximum {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	name := ".finetuning-bundle-" + uuid.NewString()
	fd, err := unix.Openat(spool, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	bundle := os.NewFile(uintptr(fd), name)
	ok := false
	defer unix.Unlinkat(spool, name, 0)
	defer func() {
		if !ok {
			bundle.Close()
		}
	}()
	digest := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(bundle, digest))
	for i, file := range files {
		if inTar[file.RelativePath] {
			member := strings.TrimPrefix(file.RelativePath, "export/merged/")
			if !biz.ValidStorageKey(member) {
				return nil, 0, "", biz.ErrInvalidWorkspaceOutput
			}
			if writer.WriteHeader(&tar.Header{Name: member, Mode: 0600, Size: file.SizeBytes, Typeflag: tar.TypeReg, Format: tar.FormatPAX}) != nil {
				return nil, 0, "", biz.ErrInvalidWorkspaceOutput
			}
			if _, err := captured[i].Seek(0, io.SeekStart); err != nil {
				return nil, 0, "", biz.ErrInvalidWorkspaceOutput
			}
			count, err := io.Copy(writer, contextReader{ctx: ctx, reader: captured[i]})
			if err != nil || count != file.SizeBytes {
				return nil, 0, "", pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
			}
		}
	}
	if writer.Close() != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	actual, err := bundle.Seek(0, io.SeekCurrent)
	if err != nil || actual != size {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	if bundle.Sync() != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	if err := unix.Linkat(spool, name, spool, "bundle.tar", 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return nil, 0, "", biz.ErrInvalidWorkspaceOutput
		}
		existing, existingSize, existingHash, _, err := originalFinetuningBundle(ctx, spool, contract, files, limits)
		return existing, existingSize, existingHash, err
	}
	if unix.Unlinkat(spool, name, 0) != nil || unix.Fsync(spool) != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	ok = true
	return bundle, size, hex.EncodeToString(digest.Sum(nil)), nil
}

// Header size comes from the same standard encoder used below, including
// PAX long-path records. File data/padding are counted without reading copies.
func modelTarSize(files []cpup01.OutputFile) (int64, error) {
	size := int64(1024)
	count := 0
	for _, file := range files {
		if !file.InTar {
			continue
		}
		count++
		if count > cpup01.MaxModelFiles || file.Role != "HF_MODEL" || !strings.HasPrefix(file.RelativePath, "export/merged/") || file.SizeBytes < 0 {
			return 0, biz.ErrInvalidWorkspaceOutput
		}
		var header bytes.Buffer
		writer := tar.NewWriter(&header)
		if err := writer.WriteHeader(&tar.Header{Name: strings.TrimPrefix(file.RelativePath, "export/merged/"), Mode: 0600, Size: file.SizeBytes, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return 0, biz.ErrInvalidWorkspaceOutput
		}
		var err error
		for _, amount := range []int64{int64(header.Len()), file.SizeBytes, (512 - file.SizeBytes%512) % 512} {
			size, err = cpup01.CheckedAdd(size, amount)
			if err != nil {
				return 0, err
			}
		}
	}
	if count == 0 {
		return 0, biz.ErrInvalidWorkspaceOutput
	}
	return size, nil
}

func originalFinetuningBundle(ctx context.Context, spool int, contract cpup01.OutputContract, files []cpup01.OutputFile, limits cpup01.ContentLimits) (*os.File, int64, string, bool, error) {
	fd, err := unix.Openat(spool, "bundle.tar", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, 0, "", false, nil
	}
	if err != nil {
		return nil, 0, "", true, biz.ErrInvalidWorkspaceOutput
	}
	file := os.NewFile(uintptr(fd), "bundle.tar")
	ok := false
	defer func() {
		if !ok {
			file.Close()
		}
	}()
	var before, after unix.Stat_t
	maximum, boundErr := cpup01.TarReadBound(limits.MaxContentBytes)
	if boundErr != nil {
		return nil, 0, "", true, biz.ErrInvalidWorkspaceOutput
	}
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size <= 0 || before.Size > maximum {
		return nil, 0, "", true, biz.ErrInvalidWorkspaceOutput
	}
	digest := sha256.New()
	reader := io.TeeReader(io.LimitReader(contextReader{ctx: ctx, reader: file}, before.Size), digest)
	if err := objectstore.ValidateFinetuningOutputTar(reader, files, contract, limits); err != nil {
		return nil, 0, "", true, pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
	}
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil || position != before.Size || unix.Fstat(fd, &after) != nil || after.Size != before.Size || after.Nlink != 1 || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return nil, 0, "", true, biz.ErrInvalidWorkspaceOutput
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", true, biz.ErrInvalidWorkspaceOutput
	}
	ok = true
	return file, before.Size, hex.EncodeToString(digest.Sum(nil)), true, nil
}

func bundlePipelineFiles(ctx context.Context, files []cpup01.OutputFile, captured []*os.File, manifest []byte) (*os.File, int64, string, error) {
	bundle, err := newPipelineTemporary()
	if err != nil {
		return nil, 0, "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = bundle.Close()
		}
	}()
	writer := tar.NewWriter(bundle)
	for i, file := range files {
		if err := writer.WriteHeader(&tar.Header{Name: file.RelativePath, Mode: 0600, Size: file.SizeBytes, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			return nil, 0, "", biz.ErrInvalidWorkspaceOutput
		}
		if _, err := captured[i].Seek(0, io.SeekStart); err != nil {
			return nil, 0, "", biz.ErrInvalidWorkspaceOutput
		}
		if _, err := io.Copy(writer, contextReader{ctx: ctx, reader: captured[i]}); err != nil {
			return nil, 0, "", pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
		}
	}
	// The manifest hashes its file members. The bundle has a separate hash and
	// never appears in its own manifest or tar, avoiding recursive identities.
	if err := writer.WriteHeader(&tar.Header{Name: "output-manifest.json", Mode: 0600, Size: int64(len(manifest)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	if _, err := writer.Write(manifest); err != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	if err := writer.Close(); err != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	digest := sha256.New()
	size, err := io.Copy(digest, contextReader{ctx: ctx, reader: bundle})
	if err != nil {
		return nil, 0, "", pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
	}
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", biz.ErrInvalidWorkspaceOutput
	}
	ok = true
	return bundle, size, hex.EncodeToString(digest.Sum(nil)), nil
}

func uploadPipelineObject(ctx context.Context, client *s3.Client, scope cpup01.StorageScope, key string, size int64, digest string, content io.ReadSeeker, phase string) (cpup01.FixedObjectRef, error) {
	if !biz.ValidStorageKey(key) || !strings.HasPrefix(key, scope.ApprovedPrefix+"/") {
		return cpup01.FixedObjectRef{}, publicationFailure(phase, "scope", biz.ErrInvalidWorkspaceOutput)
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return cpup01.FixedObjectRef{}, publicationFailure(phase, "seek", biz.ErrInvalidWorkspaceOutput)
	}
	object := cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: key, SizeBytes: size, SHA256: digest}
	response, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(scope.Bucket), Key: aws.String(key), Body: content, ContentLength: aws.Int64(size), IfNoneMatch: aws.String("*")})
	if err == nil {
		if response == nil || response.VersionId == nil || !validPipelineVersion(*response.VersionId) {
			return cpup01.FixedObjectRef{}, publicationFailure(phase, "version", biz.ErrObjectSourceUnavailable)
		}
		version := *response.VersionId
		object.VersionID = &version
		return object, nil
	}
	var apiError smithy.APIError
	if !errors.As(err, &apiError) || apiError.ErrorCode() != "PreconditionFailed" {
		return cpup01.FixedObjectRef{}, publicationStorageFailure(ctx, phase, "storage", err)
	}
	// A retry may reuse only an existing immutable version with the same actual
	// byte identity. HEAD supplies the version, never the content digest.
	existing, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(scope.Bucket), Key: aws.String(key)})
	if err != nil {
		return cpup01.FixedObjectRef{}, publicationStorageFailure(ctx, phase, "head-existing", err)
	}
	if existing == nil || existing.VersionId == nil || !validPipelineVersion(*existing.VersionId) {
		return cpup01.FixedObjectRef{}, publicationFailure(phase, "version", biz.ErrObjectVerification)
	}
	version := *existing.VersionId
	object.VersionID = &version
	verified, err := objectstore.NewVerifier(client, scope.StorageConnectionID).Verify(ctx, scope, object)
	if err != nil {
		return cpup01.FixedObjectRef{}, publicationFailure(phase, "verify-existing", err)
	}
	return verified.Object, nil
}

func validPipelineVersion(version string) bool {
	return biz.ValidStorageReference(version) && !strings.EqualFold(version, "null")
}
func pipelineContentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
func pipelineIOError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}
