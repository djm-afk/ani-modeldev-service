package modeldevwebhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CopyPlatform installs one public platform configuration from its projected
// ConfigMap. The separately frozen raw digest keeps this outside the material
// manifest, whose own digest is a field in the platform configuration.
func CopyPlatform(source, destination, expected string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || source == destination || !sha256Pattern.MatchString(expected) {
		return ErrConfiguration
	}
	f, err := os.Open(source)
	if err != nil {
		return ErrConfiguration
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	closeErr := f.Close()
	digest := sha256.Sum256(raw)
	if readErr != nil || closeErr != nil || len(raw) == 0 || len(raw) > 1<<20 || hex.EncodeToString(digest[:]) != expected {
		return ErrConfiguration
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return ErrConfiguration
	}
	if existing, openErr := os.OpenFile(destination, os.O_RDONLY|syscall.O_NOFOLLOW, 0); openErr == nil {
		info, statErr := existing.Stat()
		prior, priorErr := io.ReadAll(io.LimitReader(existing, (1<<20)+1))
		_ = existing.Close()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || priorErr != nil || sha256.Sum256(prior) != digest {
			return ErrConfiguration
		}
		return nil
	} else if !os.IsNotExist(openErr) {
		return ErrConfiguration
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".platform-")
	if err != nil {
		return ErrConfiguration
	}
	name := temp.Name()
	_, writeErr := temp.Write(raw)
	modeErr := temp.Chmod(0400)
	closeErr = temp.Close()
	if writeErr != nil || modeErr != nil || closeErr != nil {
		_ = os.Remove(name)
		return ErrConfiguration
	}
	if err = os.Rename(name, destination); err != nil {
		_ = os.Remove(name)
		return ErrConfiguration
	}
	return nil
}

// CopyMaterials is the init-container path. The expected manifest digest is a
// deployment argument, never supplied by an admission request. ConfigMap source
// symlinks are read only here; runtime readers consume regular immutable files.
func CopyMaterials(source, destination, expected string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || source == destination || !sha256Pattern.MatchString(expected) {
		return ErrConfiguration
	}
	read := func(path string) ([]byte, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, ErrConfiguration
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			return nil, ErrConfiguration
		}
		return raw, nil
	}
	raw, err := read(filepath.Join(source, "manifest.json"))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expected || !uniqueJSONKeys(raw) {
		return ErrConfiguration
	}
	var manifest struct {
		Schema string            `json:"schema"`
		Files  map[string]string `json:"files"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != "ani.modeldev.material-bundle.v1" || len(manifest.Files) != 8 {
		return ErrConfiguration
	}
	allowed := map[string]bool{"builtin.mlp.cpu.json": true, "builtin.mlp.gpu.json": true, "builtin.sft_lora.cpu.json": true, "builtin.sft_lora.gpu.json": true, "ir/modeldev-mlp-cpu.yaml": true, "ir/modeldev-mlp-gpu.yaml": true, "ir/modeldev-sft-lora-cpu.yaml": true, "ir/modeldev-sft-lora-gpu.yaml": true}
	content := map[string][]byte{"manifest.json": raw}
	total := len(raw)
	for name, wanted := range manifest.Files {
		if !allowed[name] || !sha256Pattern.MatchString(wanted) {
			return ErrConfiguration
		}
		data, err := read(filepath.Join(source, strings.ReplaceAll(name, "/", "__")))
		if err != nil {
			return err
		}
		actual := sha256.Sum256(data)
		if hex.EncodeToString(actual[:]) != wanted {
			return ErrConfiguration
		}
		total += len(data)
		if total > 8<<20 {
			return ErrConfiguration
		}
		content[name] = data
	}
	for name, data := range content {
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return ErrConfiguration
		}
		if existing, err := os.OpenFile(target, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err == nil {
			prior, _ := io.ReadAll(io.LimitReader(existing, (1<<20)+1))
			_ = existing.Close()
			a, b := sha256.Sum256(prior), sha256.Sum256(data)
			if a != b {
				return ErrConfiguration
			}
			continue
		} else if !os.IsNotExist(err) {
			return ErrConfiguration
		}
		temp, err := os.CreateTemp(filepath.Dir(target), ".material-")
		if err != nil {
			return ErrConfiguration
		}
		path := temp.Name()
		_, writeErr := temp.Write(data)
		modeErr := temp.Chmod(0400)
		closeErr := temp.Close()
		if writeErr != nil || modeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			return ErrConfiguration
		}
		if err = os.Rename(path, target); err != nil {
			_ = os.Remove(path)
			return ErrConfiguration
		}
	}
	return nil
}
