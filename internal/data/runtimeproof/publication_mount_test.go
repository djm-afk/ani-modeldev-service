package runtimeproof_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestPublicationRequiresOriginalReadOnlyPVCAndSuccessfulUploader(t *testing.T) {
	for _, attack := range []string{"valid", "writable", "replacement PVC", "wrong claim", "subpath", "sidecar writer", "uploader running"} {
		t.Run(attack, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			a := f.authority()
			association := biz.ManagedStepAssociation{RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID, PodName: "close-original", PodUID: "88888888-aaaa-4bbb-8ccc-dddddddddddd"}
			w := f.execution.Snapshot.Workspace
			workspace := biz.WorkspaceBinding{Mode: w.Mode, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(f.execution.ExecutionID), PVCUID: "99999999-aaaa-4bbb-8ccc-dddddddddddd", InputSubpath: w.InputSubpath, TrainingSubpath: w.TrainingSubpath, ReportsSubpath: w.ReportsSubpath, PublicationSubpath: w.PublicationSubpath, WorkSubpath: w.WorkSubpath, PreparedManifestSHA256: strings.Repeat("a", 64), PreparedManifestBytes: 100}
			at := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
			nodes := map[string]any{}
			tasks := []any{}
			objects := []runtime.Object{&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": a.NamespaceName, "uid": a.NamespaceUID}}}}
			var publisher *unstructured.Unstructured
			for _, role := range []string{"publish", "close"} {
				controller, nodeID := role+"-controller", role+"-node"
				phase, state := "Succeeded", "SUCCEEDED"
				if role == "close" {
					phase, state = "Running", "RUNNING"
				}
				nodes[controller] = map[string]any{"id": controller, "name": role + "-controller-name", "displayName": role, "phase": phase, "children": []any{nodeID}}
				nodes[nodeID] = map[string]any{"id": nodeID, "name": role + "-executor-name", "displayName": "executor", "phase": phase, "type": "Pod", "boundaryID": controller, "templateName": "metadata-0-2-retry-system-container-impl"}
				tasks = append(tasks, map[string]any{"run_id": a.RunID, "task_id": role + "-task", "display_name": role, "state": state, "child_tasks": []any{map[string]any{"pod_name": nodeID}}})
				uid := "77777777-aaaa-4bbb-8ccc-dddddddddddd"
				if role == "close" {
					uid = association.PodUID
				}
				pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": role + "-original", "namespace": a.NamespaceName, "uid": uid, "annotations": map[string]any{"workflows.argoproj.io/node-id": nodeID, "workflows.argoproj.io/node-name": role + "-executor-name"}, "ownerReferences": []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": a.WorkflowName, "uid": a.WorkflowUID, "controller": true}}}, "spec": map[string]any{"serviceAccountName": f.execution.Snapshot.Environment.Identities.KFPStepServiceAccount, "containers": []any{map[string]any{"name": "main"}}}, "status": map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": at}}}}}}}
				if role == "publish" {
					publisher = pod
				} else {
					mount := map[string]any{"name": "original", "mountPath": "/workspace", "readOnly": true}
					claim := workspace.PVCName
					if attack == "writable" {
						mount["readOnly"] = false
					}
					if attack == "wrong claim" {
						claim = "replacement-claim"
					}
					if attack == "subpath" {
						mount["subPath"] = "publication"
					}
					pod.Object["spec"].(map[string]any)["volumes"] = []any{map[string]any{"name": "original", "persistentVolumeClaim": map[string]any{"claimName": claim}}}
					containers := []any{map[string]any{"name": "main", "volumeMounts": []any{mount}}}
					if attack == "sidecar writer" {
						containers = append(containers, map[string]any{"name": "wait", "volumeMounts": []any{map[string]any{"name": "original", "mountPath": "/other", "readOnly": false}}})
					}
					pod.Object["spec"].(map[string]any)["containers"] = containers
				}
				objects = append(objects, pod)
			}
			if attack == "uploader running" {
				publisher.Object["status"] = map[string]any{"phase": "Running"}
			}
			_ = unstructured.SetNestedMap(f.workflow.Object, nodes, "status", "nodes")
			f.run["state"] = "RUNNING"
			f.run["run_details"] = map[string]any{"task_details": tasks}
			objects = append(objects, f.workflow)
			pvcUID := workspace.PVCUID
			if attack == "replacement PVC" {
				pvcUID = "new-pvc-uid"
			}
			objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": workspace.PVCName, "namespace": a.NamespaceName, "uid": pvcUID}, "spec": map[string]any{"storageClassName": w.StorageClass, "resources": map[string]any{"requests": map[string]any{"storage": w.CapacityBytes}}}, "status": map[string]any{"phase": "Bound"}}})
			contents := map[string][]byte{}
			scope := f.execution.Snapshot.PublicationScope
			version := "fixed-original-version"
			ref := func(key string, raw []byte) cpup01.FixedObjectRef {
				sum := sha256.Sum256(raw)
				contents["/"+scope.Bucket+"/"+key] = raw
				return cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: key, VersionID: &version, SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
			}
			candidate := biz.RuntimePublication{LogicalKey: "cpu-p01:" + f.execution.ExecutionID, Upload: biz.UploadCompletion{TaskID: "publish-task", PodUID: string(publisher.GetUID())}}
			files := []cpup01.OutputFile{}
			prefix := strings.TrimSuffix(scope.ApprovedPrefix, "/") + "/" + f.execution.ExecutionID + "/"
			for _, required := range f.execution.Snapshot.OutputContract.RequiredFiles {
				raw := []byte(required.RelativePath)
				object := ref(prefix+required.RelativePath, raw)
				file := cpup01.OutputFile{Role: required.Role, RelativePath: required.RelativePath, SizeBytes: object.SizeBytes, SHA256: object.SHA256, InTar: required.InTar}
				files = append(files, file)
				candidate.Files = append(candidate.Files, biz.PublishedRuntimeFile{File: file, Object: object})
			}
			manifest, _, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(f.execution.Admission), files)
			if err != nil {
				t.Fatal(err)
			}
			candidate.Manifest = ref(prefix+"output-manifest.json", manifest)
			var bundle bytes.Buffer
			writer := tar.NewWriter(&bundle)
			for _, file := range append(files, cpup01.OutputFile{RelativePath: "output-manifest.json", SizeBytes: int64(len(manifest))}) {
				raw := contents["/"+scope.Bucket+"/"+prefix+file.RelativePath]
				if err := writer.WriteHeader(&tar.Header{Name: file.RelativePath, Size: int64(len(raw)), Mode: 0600, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(raw); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			bundleRef := ref(prefix+"bundle.tar", bundle.Bytes())
			candidate.Bundle = &bundleRef
			remoteReads := 0
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				remoteReads++
				raw, ok := contents[r.URL.Path]
				if !ok || r.URL.Query().Get("versionId") != version {
					w.WriteHeader(404)
					return
				}
				w.Header().Set("x-amz-version-id", version)
				_, _ = w.Write(raw)
			}))
			defer peer.Close()
			client := s3.New(s3.Options{Region: "fixture", BaseEndpoint: aws.String(peer.URL), UsePathStyle: true, Credentials: aws.AnonymousCredentials{}, HTTPClient: peer.Client()})
			kube := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList"}, objects...)
			proof := runtimeproof.New(kube, f.runs, objectstore.NewVerifier(client, scope.StorageConnectionID), ownerClosePlans{f.dispatch})
			verified, err := proof.VerifyPublication(context.Background(), f.execution, association, workspace, candidate)
			if attack == "valid" {
				if err != nil || verified.Upload.PodUID != string(publisher.GetUID()) || remoteReads != len(files)+2 {
					t.Fatalf("real object and original successful uploader proof failed: %+v %v reads=%d", verified, err, remoteReads)
				}
			} else if err == nil || remoteReads != 0 {
				t.Fatalf("unproven confirmer or uploader reached publication: %v reads=%d", err, remoteReads)
			}
		})
	}
}
