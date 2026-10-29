package catalogue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func atomicMaterial(t *testing.T, generation int) (cpup01.ImplementationMaterial, []byte) {
	t.Helper()
	document := conformance.MaterialV21()
	document.Program.ImageID = fmt.Sprintf("img_%032x", generation+1)
	document.Workspace.StorageClass = fmt.Sprintf("material-generation-%d", generation)
	raw, err := document.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return document, raw
}

func replaceMaterial(t *testing.T, directory string, generation int) cpup01.ImplementationMaterial {
	t.Helper()
	document, raw := atomicMaterial(t, generation)
	next := filepath.Join(directory, "next.json")
	if err := os.WriteFile(next, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, filepath.Join(directory, "builtin.mlp.cpu.json")); err != nil {
		t.Fatal(err)
	}
	return document
}

func atomicCatalogue(t *testing.T) (string, int) {
	t.Helper()
	directory := t.TempDir()
	replaceMaterial(t, directory, 0)
	root, err := openCatalogue(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(root) })
	return directory, root
}

func TestMaterialAtomicReplacementReopensWholeDocument(t *testing.T) {
	for _, replacements := range []int{1, 2} {
		t.Run(fmt.Sprintf("replacements-%d", replacements), func(t *testing.T) {
			directory, root := atomicCatalogue(t)
			reads := 0
			want, _ := atomicMaterial(t, replacements)
			got, err := readMaterialAtWithRead(context.Background(), root, "builtin.mlp", "cpu", func(file *os.File) ([]byte, error) {
				raw, err := readMaterialBytes(file)
				reads++
				if reads <= replacements {
					replaceMaterial(t, directory, reads)
				}
				return raw, err
			})
			if err != nil || reads != replacements+1 || !reflect.DeepEqual(got, want) {
				t.Fatalf("atomic rename rejected or returned mixed material: reads=%d error=%v", reads, err)
			}
		})
	}
}

func TestMaterialAtomicReplacementHasThreeTotalReadAttempts(t *testing.T) {
	directory, root := atomicCatalogue(t)
	reads := 0
	got, err := readMaterialAtWithRead(context.Background(), root, "builtin.mlp", "cpu", func(file *os.File) ([]byte, error) {
		raw, err := readMaterialBytes(file)
		reads++
		replaceMaterial(t, directory, reads)
		return raw, err
	})
	if !errors.Is(err, ErrInvalidMaterial) || reads != 3 || !reflect.DeepEqual(got, cpup01.ImplementationMaterial{}) {
		t.Fatalf("rename retry did not stop at three total reads: reads=%d error=%v", reads, err)
	}
}

func TestMaterialAtomicReplacementBeforeFirstStatIsDetached(t *testing.T) {
	directory, _ := atomicCatalogue(t)
	file, err := os.Open(filepath.Join(directory, "builtin.mlp.cpu.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	replaceMaterial(t, directory, 1)
	reads := 0
	_, err = readOpenedMaterial(context.Background(), file, "builtin.mlp", func(file *os.File) ([]byte, error) {
		reads++
		return readMaterialBytes(file)
	})
	if !errors.Is(err, errMaterialReplaced) || reads != 0 {
		t.Fatalf("open-before-rename detached descriptor was not recognized: reads=%d error=%v", reads, err)
	}
}

func TestMaterialAtomicReplacementKeepsOriginalCancellationAndDeadline(t *testing.T) {
	for _, end := range []string{"cancel", "deadline"} {
		t.Run(end, func(t *testing.T) {
			directory, root := atomicCatalogue(t)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			reads := 0
			want := context.Canceled
			if end == "deadline" {
				want = context.DeadlineExceeded
			}
			_, err := readMaterialAtWithRead(ctx, root, "builtin.mlp", "cpu", func(file *os.File) ([]byte, error) {
				raw, err := readMaterialBytes(file)
				reads++
				replaceMaterial(t, directory, reads)
				if end == "cancel" {
					cancel()
				} else {
					<-ctx.Done()
				}
				return raw, err
			})
			if !errors.Is(err, want) || reads != 1 {
				t.Fatalf("replacement renewed the caller window: reads=%d error=%v", reads, err)
			}
		})
	}
}

func TestMaterialAtomicReplacementRejectsUnsafeAndInvalidSuccessors(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "invalid canonical", "in-place content", "in-place metadata"} {
		t.Run(kind, func(t *testing.T) {
			directory, root := atomicCatalogue(t)
			reads := 0
			_, err := readMaterialAtWithRead(context.Background(), root, "builtin.mlp", "cpu", func(file *os.File) ([]byte, error) {
				raw, err := readMaterialBytes(file)
				reads++
				if reads != 1 {
					return raw, err
				}
				name := filepath.Join(directory, "builtin.mlp.cpu.json")
				next := filepath.Join(directory, "next.json")
				_, changed := atomicMaterial(t, 1)
				switch kind {
				case "in-place content":
					if err := os.WriteFile(name, changed, 0600); err != nil {
						t.Fatal(err)
					}
					future := time.Now().Add(time.Hour)
					if err := os.Chtimes(name, future, future); err != nil {
						t.Fatal(err)
					}
					return raw, err
				case "in-place metadata":
					if err := os.Chmod(name, 0400); err != nil {
						t.Fatal(err)
					}
					return raw, err
				case "symlink", "hardlink":
					target := filepath.Join(t.TempDir(), "target.json")
					if err := os.WriteFile(target, changed, 0600); err != nil {
						t.Fatal(err)
					}
					if kind == "symlink" {
						err = os.Symlink(target, next)
					} else {
						err = os.Link(target, next)
					}
					if err != nil {
						t.Fatal(err)
					}
				case "invalid canonical":
					if err := os.WriteFile(next, append(changed, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(next, name); err != nil {
					t.Fatal(err)
				}
				return raw, err
			})
			wantReads := 1
			if kind == "invalid canonical" {
				wantReads = 2
			}
			if err == nil || reads != wantReads {
				t.Fatalf("unsafe successor accepted or in-place mutation retried: reads=%d error=%v", reads, err)
			}
		})
	}
}
