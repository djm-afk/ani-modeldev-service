package workspace_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
	"golang.org/x/sys/unix"
)

func TestPipelineIOPreparesFixedCSVAndUploadsActualBytesWithoutOverwrite(t *testing.T) {
	training, execution, contents := preparedWorkspace(t)
	csv := pipelineCSVFixture()
	execution.Snapshot.Input.Object.SizeBytes = int64(len(csv))
	execution.Snapshot.Input.Object.SHA256 = pipelineDigest(csv)
	refreshSpec(t, &execution)
	store, client := newPipelineS3(t, execution, csv)
	root := t.TempDir()
	contract := execution.Snapshot.Workspace
	binding := biz.WorkspaceBinding{Mode: contract.Mode, NamespaceName: execution.Snapshot.Environment.NamespaceName, NamespaceUID: execution.Snapshot.Environment.NamespaceUID,
		PVCName: "execution-workspace", PVCUID: "55555555-5555-4555-8555-555555555555", InputSubpath: contract.InputSubpath, TrainingSubpath: contract.TrainingSubpath,
		ReportsSubpath: contract.ReportsSubpath, PublicationSubpath: contract.PublicationSubpath, WorkSubpath: contract.WorkSubpath}
	prepared, err := workspace.PrepareInput(context.Background(), client, execution, binding, root)
	if err != nil {
		t.Fatalf("prepare real fixed input through TLS S3: %v", err)
	}
	inputBytes, err := os.ReadFile(filepath.Join(root, contract.InputSubpath, "data.csv"))
	if err != nil || !bytes.Equal(inputBytes, csv) {
		t.Fatalf("prepared input bytes differ: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, contract.ReportsSubpath, "prepared-manifest.json"))
	if err != nil || prepared.PreparedManifestSHA256 != pipelineDigest(manifest) || prepared.PreparedManifestBytes != int64(len(manifest)) {
		t.Fatalf("prepared manifest byte receipt differs: %v", err)
	}
	var manifestFacts map[string]any
	if err := json.Unmarshal(manifest, &manifestFacts); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"execution_id": execution.ExecutionID, "execution_spec_hash": execution.SpecHash, "input_sha256": execution.Snapshot.Input.Object.SHA256, "pvc_uid": binding.PVCUID} {
		if manifestFacts[key] != want {
			t.Fatalf("prepared manifest lacks %s binding", key)
		}
	}
	preparedAgain, err := workspace.PrepareInput(context.Background(), client, execution, binding, root)
	if err != nil || preparedAgain != prepared {
		t.Fatalf("prepare retry must retain same actual manifest: %v", err)
	}
	output, err := workspace.Collect(context.Background(), training, execution)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := workspace.UploadOutput(context.Background(), client, execution, training, output)
	if err != nil {
		t.Fatalf("upload real collected bytes through TLS S3: %v", err)
	}
	if candidate.ID != "" || candidate.ReceiptID != "" || !candidate.VerifiedAt.IsZero() || !candidate.Upload.CompletedAt.IsZero() {
		t.Fatal("uploader forged a publication or completion receipt")
	}
	if len(candidate.Files) != len(contents) || candidate.Bundle == nil {
		t.Fatal("uploaded candidate omitted required files or bundle")
	}
	for _, file := range candidate.Files {
		if file.ArtifactID != "" {
			t.Fatal("uploader allocated owner artifact identity")
		}
		if got := pipelineGet(t, client, file.Object); !bytes.Equal(got, contents[file.File.RelativePath]) {
			t.Fatal("fixed remote file bytes differ")
		}
	}
	if got := pipelineGet(t, client, candidate.Manifest); !bytes.Equal(got, output.Manifest) {
		t.Fatal("remote manifest differs from canonical collector manifest")
	}
	tarBytes := pipelineGet(t, client, *candidate.Bundle)
	archive := tar.NewReader(bytes.NewReader(tarBytes))
	seen := map[string]bool{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg || seen[header.Name] {
			t.Fatal("bundle contains non-regular or repeated member")
		}
		data, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		want := contents[header.Name]
		if header.Name == "output-manifest.json" {
			want = output.Manifest
		}
		if want == nil || !bytes.Equal(data, want) {
			t.Fatalf("bundle member %s differs from collected actual bytes", header.Name)
		}
		seen[header.Name] = true
	}
	if len(seen) != len(contents)+1 {
		t.Fatal("bundle lost a required file or manifest")
	}
	writes := store.writes()
	again, err := workspace.UploadOutput(context.Background(), client, execution, training, output)
	if err != nil || !reflect.DeepEqual(again, candidate) || store.writes() != writes {
		t.Fatalf("upload retry overwrote or changed fixed object versions: %v", err)
	}
	// A conditional conflict must read the actual fixed remote version, not
	// accept a forged ETag or silently repair it with another overwrite.
	store.corrupt(candidate.Files[0].Object, []byte("different bytes"))
	if _, err := workspace.UploadOutput(context.Background(), client, execution, training, output); err == nil || store.writes() != writes {
		t.Fatal("conflicting remote content was accepted or overwritten")
	}
	// Mutation after collection must not trust the earlier collector hashes.
	writeFile(t, filepath.Join(training, "model.pt"), []byte("changed after collection"))
	if _, err := workspace.UploadOutput(context.Background(), client, execution, training, output); err == nil || store.writes() != writes {
		t.Fatal("changed local file accepted after collection")
	}
}

func TestPipelineIORejectsSymlinkMountAndUnfixedInputBeforeWriting(t *testing.T) {
	_, execution, _ := preparedWorkspace(t)
	csv := pipelineCSVFixture()
	execution.Snapshot.Input.Object.SizeBytes = int64(len(csv))
	execution.Snapshot.Input.Object.SHA256 = pipelineDigest(csv)
	refreshSpec(t, &execution)
	_, client := newPipelineS3(t, execution, csv)
	contract := execution.Snapshot.Workspace
	binding := biz.WorkspaceBinding{Mode: contract.Mode, NamespaceName: execution.Snapshot.Environment.NamespaceName, NamespaceUID: execution.Snapshot.Environment.NamespaceUID,
		PVCName: "execution-workspace", PVCUID: "55555555-5555-4555-8555-555555555555", InputSubpath: contract.InputSubpath, TrainingSubpath: contract.TrainingSubpath, ReportsSubpath: contract.ReportsSubpath, PublicationSubpath: contract.PublicationSubpath, WorkSubpath: contract.WorkSubpath}
	outside, root := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, contract.InputSubpath)); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.PrepareInput(context.Background(), client, execution, binding, root); err == nil {
		t.Fatal("symlink input directory accepted")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("prepare escaped its execution mount")
	}
	execution.Snapshot.Input.Object.VersionID = nil
	execution.Snapshot.Input.Object.ImmutableCopy = aws.Bool(true)
	refreshSpec(t, &execution)
	if _, err := workspace.PrepareInput(context.Background(), client, execution, binding, t.TempDir()); err == nil {
		t.Fatal("unfixed source version accepted")
	}
}

func TestPrepareInputAtomicCreationRetryAndConflicts(t *testing.T) {
	for _, mode := range []string{"create-and-retry", "changed-input", "symlink-input", "hardlink-input", "changed-manifest"} {
		t.Run(mode, func(t *testing.T) {
			_, execution, _ := preparedWorkspace(t)
			csv := pipelineCSVFixture()
			execution.Snapshot.Input.Object.SizeBytes = int64(len(csv))
			execution.Snapshot.Input.Object.SHA256 = pipelineDigest(csv)
			refreshSpec(t, &execution)
			_, client := newPipelineS3(t, execution, csv)
			contract := execution.Snapshot.Workspace
			binding := biz.WorkspaceBinding{Mode: contract.Mode, NamespaceName: execution.Snapshot.Environment.NamespaceName, NamespaceUID: execution.Snapshot.Environment.NamespaceUID,
				PVCName: "execution-workspace", PVCUID: "55555555-5555-4555-8555-555555555555", InputSubpath: contract.InputSubpath, TrainingSubpath: contract.TrainingSubpath,
				ReportsSubpath: contract.ReportsSubpath, PublicationSubpath: contract.PublicationSubpath, WorkSubpath: contract.WorkSubpath}
			root := t.TempDir()
			inputDirectory := filepath.Join(root, contract.InputSubpath)
			reportDirectory := filepath.Join(root, contract.ReportsSubpath)
			for _, directory := range []string{inputDirectory, reportDirectory} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			inputPath := filepath.Join(inputDirectory, "data.csv")
			manifestPath := filepath.Join(reportDirectory, "prepared-manifest.json")
			original := []byte("existing bytes must survive a conflicting retry")
			protectedPath := inputPath
			switch mode {
			case "changed-input":
				writeFile(t, inputPath, original)
			case "symlink-input":
				protectedPath = filepath.Join(t.TempDir(), "outside.csv")
				writeFile(t, protectedPath, original)
				if err := os.Symlink(protectedPath, inputPath); err != nil {
					t.Fatal(err)
				}
			case "hardlink-input":
				// Matching bytes do not make a multiply linked input trustworthy.
				original = csv
				protectedPath = filepath.Join(t.TempDir(), "outside.csv")
				writeFile(t, protectedPath, original)
				if err := os.Link(protectedPath, inputPath); err != nil {
					t.Fatal(err)
				}
			case "changed-manifest":
				protectedPath = manifestPath
				writeFile(t, protectedPath, original)
			}
			prepared, err := workspace.PrepareInput(context.Background(), client, execution, binding, root)
			if mode != "create-and-retry" {
				if err == nil {
					t.Fatal("prepare accepted a conflicting or linked existing file")
				}
				retained, readErr := os.ReadFile(protectedPath)
				if readErr != nil || !bytes.Equal(retained, original) {
					t.Fatalf("prepare overwrote protected bytes: %v", readErr)
				}
			} else {
				if err != nil {
					t.Fatalf("prepare create-only publication: %v", err)
				}
				var before unix.Stat_t
				if err := unix.Stat(inputPath, &before); err != nil || before.Nlink != 1 || before.Mode&unix.S_IFMT != unix.S_IFREG {
					t.Fatalf("prepared input must be a regular single-link file: %v", err)
				}
				again, err := workspace.PrepareInput(context.Background(), client, execution, binding, root)
				if err != nil || again != prepared {
					t.Fatalf("identical prepare retry changed its receipt: %v", err)
				}
				var after unix.Stat_t
				if err := unix.Stat(inputPath, &after); err != nil || after.Ino != before.Ino || after.Nlink != 1 {
					t.Fatalf("identical retry replaced or multiply linked the original inode: %v", err)
				}
				inputBytes, err := os.ReadFile(inputPath)
				if err != nil || !bytes.Equal(inputBytes, csv) {
					t.Fatalf("prepared input differs from verified S3 bytes: %v", err)
				}
				manifest, err := os.ReadFile(manifestPath)
				if err != nil || pipelineDigest(manifest) != prepared.PreparedManifestSHA256 || int64(len(manifest)) != prepared.PreparedManifestBytes {
					t.Fatalf("prepared manifest differs from the persisted byte receipt: %v", err)
				}
			}
			for _, directory := range []string{inputDirectory, reportDirectory} {
				entries, err := os.ReadDir(directory)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".cpu-p01-") {
						t.Fatal("prepare left a named temporary file in the execution workspace")
					}
				}
			}
		})
	}
}

type pipelineS3Object struct {
	body    []byte
	version string
}
type pipelineS3Store struct {
	mu      sync.Mutex
	objects map[string]pipelineS3Object
	puts    int
	gets    int
}

func newPipelineS3(t *testing.T, execution biz.Execution, csv []byte) (*pipelineS3Store, *s3.Client) {
	t.Helper()
	input := execution.Snapshot.Input.Object
	store := &pipelineS3Store{objects: map[string]pipelineS3Object{"/" + input.Bucket + "/" + input.Key: {body: append([]byte{}, csv...), version: *input.VersionID}}}
	prefix := "/" + execution.Snapshot.PublicationScope.Bucket + "/" + execution.Snapshot.PublicationScope.ApprovedPrefix + "/" + execution.ExecutionID + "/"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		if r.Method == http.MethodGet {
			store.gets++
		}
		object, exists := store.objects[r.URL.Path]
		if r.Method == http.MethodPut {
			if !strings.HasPrefix(r.URL.Path, prefix) || r.Header.Get("If-None-Match") != "*" {
				t.Error("upload lacked approved scope or create-only condition")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if exists {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			store.puts++
			object = pipelineS3Object{body: body, version: fmt.Sprintf("output-version-%d", store.puts)}
			store.objects[r.URL.Path] = object
			w.Header().Set("x-amz-version-id", object.version)
			w.Header().Set("ETag", `"not-a-sha256"`)
			w.WriteHeader(http.StatusOK)
			return
		}
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodHead && (r.Method != http.MethodGet || r.URL.Query().Get("versionId") != object.version) {
			t.Error("object read did not fix actual version")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("x-amz-version-id", object.version)
		w.Header().Set("ETag", `"not-a-sha256"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(object.body)
		}
	}))
	t.Cleanup(server.Close)
	client := s3.New(s3.Options{Region: "test-region", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), Credentials: aws.AnonymousCredentials{}, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired})
	return store, client
}

func (store *pipelineS3Store) writes() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.puts
}
func (store *pipelineS3Store) corrupt(object cpup01.FixedObjectRef, data []byte) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := "/" + object.Bucket + "/" + object.Key
	item := store.objects[key]
	item.body = append([]byte{}, data...)
	store.objects[key] = item
}

func pipelineGet(t *testing.T, client *s3.Client, object cpup01.FixedObjectRef) []byte {
	t.Helper()
	if object.VersionID == nil {
		t.Fatal("upload returned no actual immutable object version")
	}
	response, err := client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(object.Bucket), Key: aws.String(object.Key), VersionId: object.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || int64(len(data)) != object.SizeBytes || pipelineDigest(data) != object.SHA256 {
		t.Fatalf("downloaded object differs from actual byte identity: %v", err)
	}
	return data
}

func pipelineDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func pipelineCSVFixture() []byte {
	var buffer strings.Builder
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&buffer, "x%d,", i)
	}
	buffer.WriteString("label\n")
	for row := 0; row < 1024; row++ {
		buffer.WriteString(strings.Repeat("0.25,", 16))
		fmt.Fprintf(&buffer, "%d\n", row%2)
	}
	return []byte(buffer.String())
}
