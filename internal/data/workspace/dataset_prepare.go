package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"golang.org/x/sys/unix"
)

// prepareFrozenDataset never publishes partial source or derived bytes. Both
// formats use the same trusted exact-version verifier as import, in one GET.
func prepareFrozenDataset(ctx context.Context, client *s3.Client, inputs int, input cpup01.InputRef) error {
	manifest, err := cpup01.BuildDatasetManifest(input)
	if err != nil {
		return biz.ErrInputVerification
	}
	manifestBytes, err := manifest.Canonical()
	if err != nil {
		return biz.ErrInputVerification
	}
	parent, name := inputs, "data.csv"
	if input.Format == "JSON" {
		parent, err = openPipelineSubdirectory(inputs, "data")
		if err != nil {
			return err
		}
		defer unix.Close(parent)
		name = "finetune_data.jsonl"
	}
	temporary := ".dataset-" + uuid.NewString()
	fd, err := unix.Openat(parent, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer file.Close()
	defer unix.Unlinkat(parent, temporary, 0)
	destination := &datasetDestination{file: file}
	scope := cpup01.StorageScope{StorageConnectionID: input.Object.StorageConnectionID, Bucket: input.Object.Bucket, ApprovedPrefix: path.Dir(input.Object.Key)}
	verifier := objectstore.NewVerifier(client, input.Object.StorageConnectionID)
	switch input.Format {
	case "CSV":
		proof, verifyErr := verifier.WritePreparedCSV(ctx, scope, input.Object, destination)
		err = verifyErr
		if err == nil && (proof.SchemaVersion != input.SchemaVersion || proof.RowCount != input.RowCount || proof.FeatureCount != input.FeatureCount || proof.PreparedDataset != input.PreparedDataset) {
			err = biz.ErrInputVerification
		}
	case "JSON":
		proof, verifyErr := verifier.WritePreparedAlpaca(ctx, scope, input.Object, destination)
		err = verifyErr
		if err == nil && (proof.SchemaVersion != input.SchemaVersion || proof.RowCount != input.RowCount || proof.PreparedDataset != input.PreparedDataset) {
			err = biz.ErrInputVerification
		}
	default:
		err = biz.ErrInputVerification
	}
	if destination.err != nil {
		return pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
	}
	if err != nil {
		return err
	}
	if err := file.Chmod(0444); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := file.Sync(); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := file.Close(); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Linkat(parent, temporary, parent, name, 0); err != nil && !errors.Is(err, unix.EEXIST) {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := unix.Unlinkat(parent, temporary, 0); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := verifyPreparedDatasetFile(ctx, parent, name, input.PreparedDataset); err != nil {
		return err
	}
	if err := unix.Fsync(parent); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	return writePipelineFile(ctx, inputs, "dataset-manifest.json", manifestBytes)
}

// Verification does not stage another complete dataset or use a default temp
// directory. Every byte is hashed from the exact regular, single-link inode.
func verifyPreparedDatasetFile(ctx context.Context, root int, name string, expected cpup01.PreparedDatasetFacts) error {
	fd, err := openPipelineReadFile(root, name)
	if err != nil {
		return biz.ErrInputVerification
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size != expected.SizeBytes {
		return biz.ErrInputVerification
	}
	digest := sha256.New()
	reader := contextReader{ctx: ctx, reader: file}
	size, err := io.Copy(digest, io.LimitReader(reader, expected.SizeBytes))
	var extra [1]byte
	n, tailErr := reader.Read(extra[:])
	if err != nil || size != expected.SizeBytes || n != 0 || tailErr != io.EOF || hex.EncodeToString(digest.Sum(nil)) != expected.SHA256 || unix.Fstat(fd, &after) != nil || after.Nlink != 1 || after.Size != before.Size || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return pipelineIOError(ctx, biz.ErrInputVerification)
	}
	return nil
}

type datasetDestination struct {
	file *os.File
	err  error
}

func (writer *datasetDestination) Write(value []byte) (int, error) {
	n, err := writer.file.Write(value)
	if err == nil && n != len(value) {
		err = io.ErrShortWrite
	}
	if err != nil && writer.err == nil {
		writer.err = err
	}
	return n, err
}
