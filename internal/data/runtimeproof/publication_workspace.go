package runtimeproof

import (
	"context"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (v *Verifier) verifyPublicationWorkspace(ctx context.Context, e biz.Execution, pod *unstructured.Unstructured, w biz.WorkspaceBinding) error {
	if biz.ValidateWorkspaceBinding(e, w) != nil || w.PVCName != biz.ExpectedWorkspacePVCName(e.ExecutionID) || !publicationReadOnlyMount(pod, w.PVCName) {
		return biz.ErrRuntimeConflict
	}
	pvc, err := v.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(w.NamespaceName).Get(ctx, w.PVCName, metav1.GetOptions{})
	if err != nil {
		return biz.ErrRuntimeNotReady
	}
	if !sameObject(pvc, "v1", "PersistentVolumeClaim", w.NamespaceName, w.PVCName, w.PVCUID) || pvc.GetDeletionTimestamp() != nil {
		return biz.ErrRuntimeConflict
	}
	phase, _ := textAt(pvc, "status", "phase")
	if phase != "Bound" {
		return biz.ErrRuntimeNotReady
	}
	return nil
}

// A normal publication confirmer reads only the original complete workspace.
// No other container, mount alias or volume source may turn it into a writer
// or hide the fixed candidate beneath an unrelated mount.
func publicationReadOnlyMount(pod *unstructured.Unstructured, pvc string) bool {
	if pod == nil || pvc == "" {
		return false
	}
	volumes, found, err := unstructured.NestedSlice(pod.Object, "spec", "volumes")
	if err != nil || !found {
		return false
	}
	original := ""
	names := map[string]bool{}
	for _, raw := range volumes {
		volume, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		name, ok := volume["name"].(string)
		if !ok || name == "" || names[name] {
			return false
		}
		names[name] = true
		for source := range volume {
			switch source {
			case "name", "persistentVolumeClaim", "emptyDir", "projected", "secret", "configMap", "downwardAPI":
			default:
				return false
			}
		}
		if claim, has := volume["persistentVolumeClaim"]; has {
			object, ok := claim.(map[string]any)
			if !ok || object["claimName"] != pvc || original != "" {
				return false
			}
			original = name
		}
	}
	if original == "" {
		return false
	}
	mainCount, mounted := 0, false
	for _, category := range []string{"containers", "initContainers", "ephemeralContainers"} {
		containers, _, err := unstructured.NestedSlice(pod.Object, "spec", category)
		if err != nil || category == "ephemeralContainers" && len(containers) != 0 {
			return false
		}
		for _, raw := range containers {
			container, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			main := category == "containers" && container["name"] == "main"
			if main {
				mainCount++
			}
			mounts, _, err := unstructured.NestedSlice(container, "volumeMounts")
			if err != nil {
				return false
			}
			seen := map[string]bool{}
			for _, raw := range mounts {
				mount, ok := raw.(map[string]any)
				if !ok {
					return false
				}
				name, _ := mount["name"].(string)
				path, _ := mount["mountPath"].(string)
				if !names[name] || seen[path] {
					return false
				}
				seen[path] = true
				if name == original {
					if mount["readOnly"] != true || mount["subPath"] != nil && mount["subPath"] != "" || mount["subPathExpr"] != nil && mount["subPathExpr"] != "" {
						return false
					}
					if main {
						if path != "/workspace" || mounted {
							return false
						}
						mounted = true
					}
				} else if path == "/workspace" || strings.HasPrefix(path, "/workspace/") || path == "/" {
					return false
				}
			}
		}
	}
	return mainCount == 1 && mounted
}
