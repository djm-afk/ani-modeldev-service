package objectstore

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// VerifyOutputBundle reads the immutable archive without extracting paths.
// Each regular entry must match the already frozen inventory or canonical
// manifest. Outer object SHA/size/version remain independently verified.
func (v *Verifier) VerifyOutputBundle(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, files []cpup01.OutputFile, manifest []byte) (biz.VerifiedObject, error) {
	wanted := make(map[string]cpup01.OutputFile, len(files)+1)
	for _, file := range files {
		if file.RelativePath == "output-manifest.json" || wanted[file.RelativePath].RelativePath != "" || !biz.ValidStorageKey(file.RelativePath) || file.SizeBytes < 0 {
			return biz.VerifiedObject{}, biz.ErrObjectVerification
		}
		wanted[file.RelativePath] = file
	}
	manifestDigest := sha256.Sum256(manifest)
	wanted["output-manifest.json"] = cpup01.OutputFile{RelativePath: "output-manifest.json", SizeBytes: int64(len(manifest)), SHA256: hex.EncodeToString(manifestDigest[:])}
	return v.verifyExpectedOutputTar(ctx, scope, object, wanted)
}

// VerifyFinetuningOutputBundle independently reads the fixed main model tar.
// Adapter/platform reports remain separately verified objects; only the frozen
// merged HF members may appear at the package root.
func (v *Verifier) VerifyFinetuningOutputBundle(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, files []cpup01.OutputFile, contract cpup01.OutputContract, limits cpup01.ContentLimits) (biz.VerifiedObject, error) {
	maximum, err := cpup01.TarReadBound(limits.MaxContentBytes)
	if err != nil || object.SizeBytes > maximum {
		return biz.VerifiedObject{}, biz.ErrObjectVerification
	}
	wanted, err := finetuningTarMembers(files, contract, limits)
	if err != nil {
		return biz.VerifiedObject{}, err
	}
	return v.verify(ctx, scope, object, func(stream io.Reader) error { return validateFinetuningTar(stream, wanted, limits) })
}

// ValidateFinetuningOutputTar verifies the actual merged model archive without
// counting its bounded POSIX/PAX container overhead as model content.
func ValidateFinetuningOutputTar(stream io.Reader, files []cpup01.OutputFile, contract cpup01.OutputContract, limits cpup01.ContentLimits) error {
	wanted, err := finetuningTarMembers(files, contract, limits)
	if err != nil {
		return err
	}
	return validateFinetuningTar(stream, wanted, limits)
}

func validateFinetuningTar(stream io.Reader, wanted map[string]cpup01.OutputFile, limits cpup01.ContentLimits) error {
	maximum, err := cpup01.TarReadBound(limits.MaxContentBytes)
	if err != nil {
		return biz.ErrObjectVerification
	}
	minimum := int64(1024)
	var payload int64
	for _, file := range wanted {
		padding := (512 - file.SizeBytes%512) % 512
		for _, size := range []int64{512, file.SizeBytes, padding} {
			minimum, err = cpup01.CheckedAdd(minimum, size)
			if err != nil {
				return biz.ErrObjectVerification
			}
		}
		payload, err = cpup01.CheckedAdd(payload, file.SizeBytes)
		if err != nil || payload > limits.MaxContentBytes {
			return biz.ErrObjectVerification
		}
	}
	measured := &measuredReader{reader: io.LimitReader(stream, maximum)}
	if err := verifyOutputTar(measured, wanted); err != nil {
		return err
	}
	var extra [1]byte
	n, probeErr := stream.Read(extra[:])
	if n != 0 || probeErr != io.EOF || measured.size < minimum || measured.size-payload > maximum-limits.MaxContentBytes {
		return biz.ErrObjectVerification
	}
	return nil
}

func finetuningTarMembers(files []cpup01.OutputFile, contract cpup01.OutputContract, limits cpup01.ContentLimits) (map[string]cpup01.OutputFile, error) {
	if contract.SchemaVersion != cpup01.OutputContractSchemaVersion || contract.OutputKind != "MERGED_HF_MODEL" || !contract.CreateTarBundle || len(files) == 0 || len(files) > cpup01.MaxOutputFiles || limits.Validate() != nil {
		return nil, biz.ErrObjectVerification
	}
	wanted := map[string]cpup01.OutputFile{}
	seen := map[string]bool{}
	var total int64
	for _, file := range files {
		if seen[file.RelativePath] || !biz.ValidStorageKey(file.RelativePath) || len(file.RelativePath) > cpup01.MaxRelativePathBytes || file.SizeBytes < 0 {
			return nil, biz.ErrObjectVerification
		}
		seen[file.RelativePath] = true
		var err error
		total, err = cpup01.CheckedAdd(total, file.SizeBytes)
		if err != nil || total > limits.MaxContentBytes {
			return nil, biz.ErrObjectVerification
		}
		if file.InTar {
			if file.Role != "HF_MODEL" || !strings.HasPrefix(file.RelativePath, "export/merged/") {
				return nil, biz.ErrObjectVerification
			}
			name := strings.TrimPrefix(file.RelativePath, "export/merged/")
			if !biz.ValidStorageKey(name) || len(wanted) >= cpup01.MaxModelFiles {
				return nil, biz.ErrObjectVerification
			}
			file.RelativePath = name
			wanted[name] = file
		}
	}
	if len(wanted) == 0 {
		return nil, biz.ErrObjectVerification
	}
	return wanted, nil
}

func (v *Verifier) verifyExpectedOutputTar(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, wanted map[string]cpup01.OutputFile) (biz.VerifiedObject, error) {
	return v.verify(ctx, scope, object, func(stream io.Reader) error { return verifyOutputTar(stream, wanted) })
}

func verifyOutputTar(stream io.Reader, wanted map[string]cpup01.OutputFile) error {
	reader := tar.NewReader(stream)
	seen := make(map[string]bool, len(wanted))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return biz.ErrObjectVerification
		}
		file, ok := wanted[header.Name]
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Linkname != "" || header.Size != file.SizeBytes || (header.Format != tar.FormatUSTAR && header.Format != tar.FormatPAX) {
			return biz.ErrObjectVerification
		}
		for name := range header.PAXRecords {
			if name != "path" {
				return biz.ErrObjectVerification
			}
		}
		seen[header.Name] = true
		digest := sha256.New()
		count, err := io.Copy(digest, reader)
		if err != nil || count != file.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != file.SHA256 {
			return biz.ErrObjectVerification
		}
	}
	if len(seen) != len(wanted) {
		return biz.ErrObjectVerification
	}
	// tar permits zero padding after its end marker; nonzero trailing bytes
	// would conceal a second archive or unrelated payload.
	var padding [4096]byte
	for {
		count, err := stream.Read(padding[:])
		for _, value := range padding[:count] {
			if value != 0 {
				return biz.ErrObjectVerification
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil || count == 0 {
			return biz.ErrObjectVerification
		}
	}
}
