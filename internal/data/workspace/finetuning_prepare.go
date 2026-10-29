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
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"golang.org/x/sys/unix"
)

const FinetuningDatasetInfo = `{"ani_cpu_smoke":{"file_name":"finetune_data.jsonl","formatting":"alpaca","columns":{"prompt":"instruction","query":"input","response":"output"}}}`

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
	if binding.Mode != want.Mode || binding.NamespaceName != e.Snapshot.Environment.NamespaceName || binding.NamespaceUID != e.Snapshot.Environment.NamespaceUID || binding.PVCName == "" || binding.PVCUID == "" || binding.InputSubpath != want.InputSubpath || binding.TrainingSubpath != want.TrainingSubpath || binding.ReportsSubpath != want.ReportsSubpath || binding.PublicationSubpath != want.PublicationSubpath || binding.WorkSubpath != want.WorkSubpath {
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
	if err := prepareWorkDirectories(ctx, root, e.Snapshot.Workspace.WorkSubpath); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	fine := e.Snapshot.Finetuning
	inputs, err := openPipelineSubdirectory(root, e.Snapshot.Workspace.InputSubpath)
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(inputs)
	if err := prepareFrozenDataset(ctx, dataClient, inputs, e.Snapshot.Input); err != nil {
		return biz.WorkspaceBinding{}, err
	}
	dataDir, err := openPipelineSubdirectory(inputs, "data")
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	defer unix.Close(dataDir)
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
	plan, err := fine.TemplatePlan.Canonical()
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	capabilities, err := fine.RuntimeCapabilities.Canonical()
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	for name, raw := range map[string][]byte{"template-plan.json": plan, "runtime-capabilities.json": capabilities} {
		if err := writePipelineFile(ctx, inputs, name, raw); err != nil {
			return biz.WorkspaceBinding{}, err
		}
	}
	baseManifest, err := fine.BaseModel.BaseManifestBytes()
	if err != nil {
		return biz.WorkspaceBinding{}, err
	}
	if err := prepareBaseArchive(ctx, baseClient, inputs, fine.BaseModel, e.Snapshot.Limits); err != nil {
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
	fine := e.Snapshot.Finetuning
	reports, err := openPipelineSubdirectory(root, e.Snapshot.Workspace.ReportsSubpath)
	if err != nil {
		return false, nil, err
	}
	plan, err := fine.TemplatePlan.Canonical()
	if err != nil {
		return false, nil, err
	}
	capabilities, err := fine.RuntimeCapabilities.Canonical()
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
	if err := verifyPreparedDatasetFile(ctx, root, input+"/data/finetune_data.jsonl", e.Snapshot.Input.PreparedDataset); err != nil {
		return false, nil, err
	}
	dataset, err := cpup01.BuildDatasetManifest(e.Snapshot.Input)
	if err != nil {
		return false, nil, err
	}
	datasetBytes, err := dataset.Canonical()
	if err != nil {
		return false, nil, err
	}
	files := []cpup01.OutputFile{}
	for _, file := range []struct {
		name string
		data []byte
	}{
		{input + "/data/dataset_info.json", []byte(FinetuningDatasetInfo)},
		{input + "/dataset-manifest.json", datasetBytes},
		{input + "/finetune_configuration.json", configuration},
		{input + "/template-plan.json", plan},
		{input + "/runtime-capabilities.json", capabilities},
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

func prepareBaseArchive(ctx context.Context, client *s3.Client, inputs int, base cpup01.BaseModelRef, limits cpup01.ContentLimits) error {
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
	bound, err := cpup01.TarReadBound(limits.MaxContentBytes)
	if err != nil || o.SizeBytes <= 0 || o.SizeBytes > bound {
		return biz.ErrInputVerification
	}
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
	reader := contextReader{ctx: ctx, reader: response.Body}
	size, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(reader, o.SizeBytes))
	if err != nil {
		return pipelineIOError(ctx, biz.ErrInputSourceUnavailable)
	}
	var extra [1]byte
	n, tailErr := reader.Read(extra[:])
	if size != o.SizeBytes || n != 0 || tailErr != io.EOF || hex.EncodeToString(hash.Sum(nil)) != o.SHA256 {
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
	if err := extractBaseModel(ctx, archive, model, base, limits.MaxContentBytes); err != nil {
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
	directories := make(map[string]bool)
	for _, file := range base.Files {
		expected[file.Path] = file
		for dir := path.Dir(file.Path); dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
		}
	}
	seen := make(map[string]bool, len(expected))
	seenEntries := make(map[string]bool)
	maximum, err := cpup01.TarReadBound(limit)
	if err != nil {
		return biz.ErrInputVerification
	}
	overhead := maximum - limit
	measured := &baseArchiveCounter{source: contextReader{ctx: ctx, reader: archive}}
	reader := tar.NewReader(measured)
	var payload int64
	entries := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return biz.ErrInputVerification
		}
		entries++
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if measured.bytes-payload > overhead || entries > cpup01.MaxArchiveEntries || (header.Format != tar.FormatUSTAR && header.Format != tar.FormatPAX) || !biz.ValidStorageKey(name) || len(name) > cpup01.MaxRelativePathBytes || strings.Contains(name, "\\") || seenEntries[name] || header.Linkname != "" {
			return biz.ErrInputVerification
		}
		seenEntries[name] = true
		for key, value := range header.PAXRecords {
			if key == "path" && value == header.Name || key == "size" && value == strconv.FormatInt(header.Size, 10) {
				continue
			}
			return biz.ErrInputVerification
		}
		if header.Typeflag == tar.TypeDir {
			if !directories[name] || header.Size != 0 {
				return biz.ErrInputVerification
			}
			fd, err := openPipelineSubdirectory(root, name)
			if err != nil {
				return err
			}
			unix.Close(fd)
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return biz.ErrInputVerification
		}
		want, ok := expected[header.Name]
		if !ok || header.Size != want.SizeBytes || header.Size < 0 || header.Size > limit {
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
		payload, err = cpup01.CheckedAdd(payload, n)
		syncErr := file.Sync()
		modeErr := file.Chmod(0444)
		closeErr := file.Close()
		if err != nil || measured.bytes-payload > overhead || copyErr != nil || syncErr != nil || modeErr != nil || closeErr != nil || n != want.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
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
		n, err := measured.Read(buffer)
		if measured.bytes-payload > overhead {
			return biz.ErrInputVerification
		}
		for _, b := range buffer[:n] {
			if b != 0 {
				return biz.ErrInputVerification
			}
		}
		if err == io.EOF {
			if measured.bytes != base.Object.SizeBytes {
				return biz.ErrInputVerification
			}
			break
		}
		if err != nil {
			return biz.ErrInputVerification
		}
	}
	return nil
}

// Count all bytes beneath the standard reader, including hidden PAX payload,
// directory headers, alignment and zero tail. Ordinary content is accounted
// separately when tar.Reader exposes and we actually consume each file.
type baseArchiveCounter struct {
	source io.Reader
	bytes  int64
}

func (r *baseArchiveCounter) Read(buffer []byte) (int, error) {
	n, err := r.source.Read(buffer)
	next, countErr := cpup01.CheckedAdd(r.bytes, int64(n))
	if countErr != nil {
		return n, countErr
	}
	r.bytes = next
	return n, err
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
	reader := contextReader{ctx: ctx, reader: file}
	n, err := io.Copy(h, io.LimitReader(reader, want.SizeBytes))
	var extra [1]byte
	tail, tailErr := reader.Read(extra[:])
	var after unix.Stat_t
	return err == nil && tail == 0 && tailErr == io.EOF && n == want.SizeBytes && hex.EncodeToString(h.Sum(nil)) == want.SHA256 && unix.Fstat(fd, &after) == nil && after.Nlink == 1 && before.Size == after.Size && before.Mtim == after.Mtim && before.Ctim == after.Ctim, err
}
