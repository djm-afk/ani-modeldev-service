package objectstore

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
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
		if file.RelativePath == "output-manifest.json" || wanted[file.RelativePath].RelativePath != "" || !biz.ValidStorageKey(file.RelativePath) || file.SizeBytes <= 0 {
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
func (v *Verifier) VerifyFinetuningOutputBundle(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, files []cpup01.OutputFile, contract cpup01.OutputContract) (biz.VerifiedObject, error) {
	if contract.MaxTarBytes == nil || *contract.MaxTarBytes <= 0 || object.SizeBytes > *contract.MaxTarBytes {
		return biz.VerifiedObject{}, biz.ErrObjectVerification
	}
	wanted, err := finetuningTarMembers(files, contract)
	if err != nil {
		return biz.VerifiedObject{}, err
	}
	return v.verify(ctx, scope, object, func(stream io.Reader) error { return validateFinetuningTar(stream, wanted, *contract.MaxTarBytes) })
}

// ValidateFinetuningOutputTar is also used to validate the original durable
// workspace tar on publication retries. It performs no extraction or network
// I/O, and callers separately enforce the stream's byte budget and identity.
func ValidateFinetuningOutputTar(stream io.Reader, files []cpup01.OutputFile, contract cpup01.OutputContract) error {
	wanted, err := finetuningTarMembers(files, contract)
	if err != nil {
		return err
	}
	return validateFinetuningTar(stream, wanted, *contract.MaxTarBytes)
}

func validateFinetuningTar(stream io.Reader, wanted map[string]cpup01.OutputFile, maximum int64) error {
	minimum := int64(1024)
	for _, file := range wanted {
		if file.SizeBytes <= 0 || file.SizeBytes > math.MaxInt64-1023 {
			return biz.ErrObjectVerification
		}
		memberBytes := int64(512) + (file.SizeBytes+511)/512*512
		if memberBytes > maximum-minimum {
			return biz.ErrObjectVerification
		}
		minimum += memberBytes
	}
	measured := &measuredReader{reader: io.LimitReader(stream, maximum+1)}
	if err := verifyOutputTar(measured, wanted); err != nil {
		return err
	}
	if measured.size < minimum || measured.size > maximum {
		return biz.ErrObjectVerification
	}
	return nil
}

func finetuningTarMembers(files []cpup01.OutputFile, contract cpup01.OutputContract) (map[string]cpup01.OutputFile, error) {
	if contract.SchemaVersion != "ani.finetune.output.v1" || contract.OutputKind != "MERGED_HF_MODEL" || !contract.CreateTarBundle || contract.MaxTarBytes == nil || *contract.MaxTarBytes <= 0 || len(files) != len(contract.RequiredFiles) || len(files) > 256 {
		return nil, biz.ErrObjectVerification
	}
	wanted := map[string]cpup01.OutputFile{}
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file.RelativePath] || !biz.ValidStorageKey(file.RelativePath) || file.SizeBytes <= 0 {
			return nil, biz.ErrObjectVerification
		}
		seen[file.RelativePath] = true
		found := false
		for _, required := range contract.RequiredFiles {
			if required.RelativePath == file.RelativePath {
				if required.Role != file.Role || file.SizeBytes > required.MaxSizeBytes || required.InTar == nil {
					return nil, biz.ErrObjectVerification
				}
				found = true
				if *required.InTar {
					if required.Role != "HF_MODEL" || !strings.HasPrefix(file.RelativePath, "export/merged/") {
						return nil, biz.ErrObjectVerification
					}
					name := strings.TrimPrefix(file.RelativePath, "export/merged/")
					if !biz.ValidStorageKey(name) || wanted[name].RelativePath != "" {
						return nil, biz.ErrObjectVerification
					}
					file.RelativePath = name
					wanted[name] = file
				}
				break
			}
		}
		if !found {
			return nil, biz.ErrObjectVerification
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
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Linkname != "" || header.Size != file.SizeBytes || header.Format != tar.FormatUSTAR || len(header.PAXRecords) != 0 {
			return biz.ErrObjectVerification
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
