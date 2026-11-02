package tenantenvironment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func resource(kind string) schema.GroupVersionResource {
	switch kind {
	case "Namespace":
		return schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	case "ServiceAccount":
		return schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	case "ConfigMap":
		return schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	case "Secret":
		return schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	case "RoleBinding":
		return schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	case "ClusterRole":
		return schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	case "ValidatingAdmissionPolicy":
		return schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"}
	case "ValidatingAdmissionPolicyBinding":
		return schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"}
	case "MutatingWebhookConfiguration":
		return schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	}
	return schema.GroupVersionResource{}
}
func (p *Platform) get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return p.kube.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// These are actual responses to this service account's request. No other
// principal's grants or the cluster's authorization graph are inferred here.
func kubernetesFailure(err error) error {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return biz.EnvironmentError("ENVIRONMENT_AUTHORIZATION_UNVERIFIED")
	}
	if apierrors.IsInvalid(err) {
		return biz.EnvironmentError("INCOMPATIBLE_ADMISSION_POLICY")
	}
	return biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
}

func originalObjectFailure(err error) error {
	if apierrors.IsNotFound(err) {
		return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
	}
	return kubernetesFailure(err)
}
func (p *Platform) VerifyNamespace(ctx context.Context, row *biz.TenantExecutionEnvironment) error {
	name, err := biz.TenantNamespace(row.TenantID)
	if err != nil || name != row.NamespaceName || row.ClusterID != p.config.ClusterID {
		return biz.EnvironmentConflict("TENANT_NAMESPACE_OWNERSHIP_MISMATCH")
	}
	o, err := p.get(ctx, resource("Namespace"), "", name)
	if apierrors.IsNotFound(err) {
		return biz.EnvironmentError("TENANT_NAMESPACE_NOT_FOUND")
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return biz.EnvironmentError("TENANT_NAMESPACE_ACCESS_DENIED")
	}
	if err != nil {
		return biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	labels := o.GetLabels()
	if o.GetName() != name || labels["network.ani.io/tenant-id"] != row.TenantID || labels["network.ani.io/managed-by"] != "ani-network-service" || !biz.CanonicalEnvironmentUUID(string(o.GetUID())) {
		return biz.EnvironmentConflict("TENANT_NAMESPACE_OWNERSHIP_MISMATCH")
	}
	if row.NamespaceUID != "" && row.NamespaceUID != string(o.GetUID()) {
		return biz.EnvironmentConflict("TENANT_NAMESPACE_UID_CHANGED")
	}
	phase, _, _ := unstructured.NestedString(o.Object, "status", "phase")
	if phase != "Active" || o.GetDeletionTimestamp() != nil {
		return biz.EnvironmentError("TENANT_NAMESPACE_NOT_ACTIVE")
	}
	row.NamespaceUID = string(o.GetUID())
	return nil
}
func ownerLabels(row *biz.TenantExecutionEnvironment) map[string]string {
	return map[string]string{"ani.io/managed-by": "ani-modeldev-service", "modeldev.ani.io/environment-id": row.ID, "modeldev.ani.io/tenant-id": row.TenantID}
}
func object(row *biz.TenantExecutionEnvironment, kind, name string, fields map[string]any) *unstructured.Unstructured {
	apiVersion := "v1"
	if kind == "RoleBinding" {
		apiVersion = "rbac.authorization.k8s.io/v1"
	}
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind}}
	o.SetName(name)
	o.SetNamespace(row.NamespaceName)
	o.SetLabels(ownerLabels(row))
	o.SetAnnotations(map[string]string{"modeldev.ani.io/namespace-uid": row.NamespaceUID})
	for k, v := range fields {
		o.Object[k] = v
	}
	return o
}
func objectDigest(o *unstructured.Unstructured) string {
	v := o.DeepCopy()
	v.SetUID("")
	v.SetResourceVersion("")
	v.SetCreationTimestamp(metav1.Time{})
	delete(v.Object, "status")
	m, _, _ := unstructured.NestedMap(v.Object, "metadata")
	for _, k := range []string{"uid", "resourceVersion", "creationTimestamp", "managedFields", "generation"} {
		delete(m, k)
	}
	v.Object["metadata"] = m
	raw, _ := json.Marshal(v.Object)
	return biz.EnvironmentDigest(raw)
}

// BootstrapDigest is the canonical digest used by deployment evidence. UID is
// pinned separately. It includes the entire public spec/rules/subjects.
func BootstrapDigest(o *unstructured.Unstructured) string { return objectDigest(o) }
func sameOwner(actual, expected *unstructured.Unstructured) bool {
	if actual.GetKind() != expected.GetKind() || actual.GetNamespace() != expected.GetNamespace() || actual.GetName() != expected.GetName() || actual.GetDeletionTimestamp() != nil || !biz.CanonicalEnvironmentUUID(string(actual.GetUID())) {
		return false
	}
	for k, v := range expected.GetLabels() {
		if actual.GetLabels()[k] != v {
			return false
		}
	}
	for k, v := range expected.GetAnnotations() {
		if actual.GetAnnotations()[k] != v {
			return false
		}
	}
	return true
}
func sameManaged(actual, expected *unstructured.Unstructured) bool {
	if !sameOwner(actual, expected) {
		return false
	}
	for k, v := range expected.Object {
		if k == "metadata" || k == "apiVersion" || k == "kind" {
			continue
		}
		if k == "imagePullSecrets" {
			if values, ok := v.([]any); ok && len(values) == 0 {
				actualValues, _, err := unstructured.NestedSlice(actual.Object, k)
				if err != nil || len(actualValues) != 0 {
					return false
				}
				continue
			}
		}
		if !reflect.DeepEqual(actual.Object[k], v) {
			return false
		}
	}
	return true
}
func receipt(row *biz.TenantExecutionEnvironment, kind, name string) *biz.EnvironmentObjectReceipt {
	for i := range row.Receipts.Objects {
		r := &row.Receipts.Objects[i]
		if r.Kind == kind && r.Name == name {
			return r
		}
	}
	return nil
}
func (p *Platform) ensureObject(ctx context.Context, row *biz.TenantExecutionEnvironment, expected *unstructured.Unstructured, checkpoint func() error) error {
	kind, name := expected.GetKind(), expected.GetName()
	digest := objectDigest(expected)
	secret := kind == "Secret"
	if secret {
		digest = biz.EnvironmentDigest([]byte("Opaque:access_key,secret_key"))
	}
	r := receipt(row, kind, name)
	if r == nil {
		row.Receipts.Objects = append(row.Receipts.Objects, biz.EnvironmentObjectReceipt{Kind: kind, Name: name, SpecDigest: digest})
		if err := checkpoint(); err != nil {
			return err
		}
		r = receipt(row, kind, name)
	} else if r.SpecDigest != digest {
		return biz.EnvironmentConflict("ENVIRONMENT_CONFIG_DRIFT")
	}
	if err := p.VerifyNamespace(ctx, row); err != nil {
		return err
	}
	actual, err := p.get(ctx, resource(kind), row.NamespaceName, name)
	if apierrors.IsNotFound(err) {
		if r.UID != "" {
			return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, createErr := p.kube.Resource(resource(kind)).Namespace(row.NamespaceName).Create(call, expected, metav1.CreateOptions{})
		cancel()
		// An authoritative native refusal cannot be recovered by observing a
		// concurrent object's existence. Only an uncertain response retains the
		// original readback recovery path below.
		if apierrors.IsForbidden(createErr) || apierrors.IsUnauthorized(createErr) || apierrors.IsInvalid(createErr) {
			return kubernetesFailure(createErr)
		}
		actual, err = p.get(ctx, resource(kind), row.NamespaceName, name)
	}
	if err != nil {
		return kubernetesFailure(err)
	}
	if !sameOwner(actual, expected) || (r.UID != "" && r.UID != string(actual.GetUID())) {
		return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
	}
	if secret {
		if actual.Object["type"] != "Opaque" {
			return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
		}
		data, ok, _ := unstructured.NestedStringMap(actual.Object, "data")
		if !ok || len(data) != 2 || data["access_key"] == "" || data["secret_key"] == "" {
			return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
		}
		if !reflect.DeepEqual(actual.Object["data"], expected.Object["data"]) {
			if err := p.VerifyNamespace(ctx, row); err != nil {
				return err
			}
			patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"uid": string(actual.GetUID()), "resourceVersion": actual.GetResourceVersion()}, "data": expected.Object["data"]})
			call, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err = p.kube.Resource(resource(kind)).Namespace(row.NamespaceName).Patch(call, name, types.MergePatchType, patch, metav1.PatchOptions{})
			cancel()
			if err != nil {
				return kubernetesFailure(err)
			}
			actual, err = p.get(ctx, resource(kind), row.NamespaceName, name)
		}
	}
	if err != nil {
		return originalObjectFailure(err)
	}
	if !sameManaged(actual, expected) {
		return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
	}
	if err = p.VerifyNamespace(ctx, row); err != nil {
		return err
	}
	r.UID = string(actual.GetUID())
	r.Confirmed = true
	r.CheckedAt = time.Now().UTC()
	return checkpoint()
}
func stringMap(v map[string]string) map[string]any {
	out := map[string]any{}
	for k, s := range v {
		out[k] = s
	}
	return out
}
func (p *Platform) publicObjects(row *biz.TenantExecutionEnvironment) ([]*unstructured.Unstructured, error) {
	c := p.config
	refs := []any{map[string]any{"name": row.Receipts.ImageSecret}}
	out := []*unstructured.Unstructured{}
	for _, name := range []string{"modeldev-step", "trainer-workload", "verifier"} {
		fields := map[string]any{"imagePullSecrets": refs}
		if name == "trainer-workload" {
			fields["automountServiceAccountToken"] = false
			fields["imagePullSecrets"] = []any{}
		}
		out = append(out, object(row, "ServiceAccount", name, fields))
	}
	for _, rb := range []struct{ name, role, ns, sa string }{{"modeldev-runtime", "modeldev-runtime-v1", "ani-system", "modeldev-control"}, {"modeldev-step", "modeldev-step-v1", row.NamespaceName, "modeldev-step"}, {"modeldev-webhook-read", "modeldev-webhook-read-v1", "ani-system", "ani-modeldev-webhook"}, {"modeldev-kfp-api", "modeldev-kfp-api-v1", c.KFPAPIServiceAccount.Namespace, c.KFPAPIServiceAccount.Name}, {"modeldev-kfp-access", "modeldev-kfp-access-v1", "ani-system", "modeldev-control"}} {
		out = append(out, object(row, "RoleBinding", rb.name, map[string]any{"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": rb.role}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": rb.sa, "namespace": rb.ns}}}))
	}
	stepRaw, _ := json.Marshal(c.StepOwner)
	var step map[string]any
	_ = json.Unmarshal(stepRaw, &step)
	step["tenant_id"] = row.TenantID
	step["namespace_uid"] = row.NamespaceUID
	stepRaw, _ = json.Marshal(step)
	providers, _ := json.Marshal(map[string]any{"s3": map[string]any{"default": map[string]any{"endpoint": c.ArtifactEndpoint, "region": c.ArtifactRegion, "disableSSL": false, "forcePathStyle": true, "maxRetries": 3, "credentials": map[string]any{"fromEnv": false, "secretRef": map[string]any{"secretName": "modeldev-kfp-artifacts", "accessKeyKey": "access_key", "secretKeyKey": "secret_key"}}}}})
	for _, cm := range []struct {
		name string
		data map[string]string
	}{{"modeldev-step-owner", map[string]string{"config.json": string(stepRaw), "modeldev-ca.pem": c.ModeldevPublicCA, "s3-ca.pem": c.S3PublicCA}}, {"ani-kfp-ca", map[string]string{c.KFPCAKey: c.KFPPublicCA}}, {"kfp-launcher", map[string]string{"providers": string(providers), "defaultPipelineRoot": "s3://" + c.ArtifactBucket + "/artifacts/" + row.TenantID}}, {"metadata-grpc-configmap", map[string]string{"METADATA_GRPC_SERVICE_HOST": c.MetadataHost, "METADATA_GRPC_SERVICE_PORT": c.MetadataPort}}} {
		out = append(out, object(row, "ConfigMap", cm.name, map[string]any{"immutable": true, "data": stringMap(cm.data)}))
	}
	return out, nil
}
func (p *Platform) Resources(ctx context.Context, row *biz.TenantExecutionEnvironment, checkpoint func() error) error {
	// Native legacy-token and Pod protections are required before creating SAs.
	if err := p.verifyBootstrap(ctx); err != nil {
		return err
	}
	if err := p.VerifyNamespace(ctx, row); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	secret, err := p.images.EnsurePullSecret(call, row.TenantID, row.ClusterID, row.NamespaceName, row.NamespaceUID, p.config.RegistryAuthority)
	cancel()
	if err != nil || secret == "" {
		return biz.EnvironmentError("TENANT_IMAGE_SPACE_NOT_READY")
	}
	if row.Receipts.ImageSecret != "" && row.Receipts.ImageSecret != secret {
		return biz.EnvironmentConflict("ENVIRONMENT_CONFIG_DRIFT")
	}
	row.Receipts.ImageSecret = secret
	if err = checkpoint(); err != nil {
		return err
	}
	objects, err := p.publicObjects(row)
	if err != nil {
		return err
	}
	for _, o := range objects {
		if err = p.ensureObject(ctx, row, o, checkpoint); err != nil {
			return err
		}
	}
	ak, err := readFile(p.config.ArtifactAccessKeyFile, 16<<10)
	if err != nil || len(ak) == 0 {
		return biz.EnvironmentError("KFP_ARTIFACT_CONFIGURATION_NOT_READY")
	}
	sk, err := readFile(p.config.ArtifactSecretKeyFile, 16<<10)
	if err != nil || len(sk) == 0 {
		return biz.EnvironmentError("KFP_ARTIFACT_CONFIGURATION_NOT_READY")
	}
	defer clear(ak)
	defer clear(sk)
	o := object(row, "Secret", "modeldev-kfp-artifacts", map[string]any{"type": "Opaque", "data": map[string]any{"access_key": base64.StdEncoding.EncodeToString(ak), "secret_key": base64.StdEncoding.EncodeToString(sk)}})
	return p.ensureObject(ctx, row, o, checkpoint)
}
func (p *Platform) Seal(ctx context.Context, row *biz.TenantExecutionEnvironment) ([]byte, error) {
	c := p.config
	input, err := cpup01.TenantStorageScope(c.InputScope, row.TenantID, "input")
	if err != nil {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	publish, err := cpup01.TenantStorageScope(c.PublicationScope, row.TenantID, "publish")
	if err != nil {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	identities := cpup01.RuntimeIdentityRefs{ModeldevControlIdentityRef: "system:serviceaccount:ani-system:modeldev-control", KFPStepServiceAccount: "modeldev-step", TrainerServiceAccount: "trainer-workload", VerifierServiceAccount: "verifier", TenantProxyIdentity: "system:serviceaccount:ani-system:modeldev-control"}
	for _, r := range row.Receipts.Objects {
		if !r.Confirmed {
			return nil, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
		}
		switch r.Name {
		case "modeldev-step":
			if r.Kind == "ServiceAccount" {
				identities.KFPStepServiceAccountUID = r.UID
			}
		case "trainer-workload":
			identities.TrainerServiceAccountUID = r.UID
		case "verifier":
			identities.VerifierServiceAccountUID = r.UID
		}
	}
	owner := c.DispatchOwner
	owner.PipelineRoot = "s3://" + c.ArtifactBucket + "/artifacts/" + row.TenantID
	b := projection.Binding{SchemaVersion: projection.Schema, TenantID: row.TenantID, Environment: projection.Environment{BindingID: row.ID, ClusterID: row.ClusterID, NamespaceName: row.NamespaceName, NamespaceUID: row.NamespaceUID, KFPConnectionRef: row.KFPConnectionRef, ExperimentID: row.ExperimentID, Identities: identities}, InputScope: input, PublicationScope: publish, Workspace: c.Workspace, Timing: c.Timing, InputFilePath: c.InputFilePath, OutputDirectoryPath: c.OutputDirectoryPath, TargetPlatform: c.TargetPlatform, ManagedImageAccess: cpup01.ManagedImageAccessSnapshot{PullBindings: []cpup01.ManagedImagePullBinding{{RegistryAuthority: c.RegistryAuthority, SecretName: row.Receipts.ImageSecret}}}, DispatchOwner: owner, MaterialBundleDigest: c.MaterialBundleDigest, Profiles: c.Profiles, ControllerPrincipals: c.ControllerPrincipals, Objects: []projection.ObjectRef{}}
	b.ModelScope, err = cpup01.TenantStorageScope(c.ModelScope, row.TenantID, "base-model")
	if err != nil {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	for _, r := range row.Receipts.Objects {
		if r.Kind != "Secret" && r.Name != "modeldev-execution-environment" {
			b.Objects = append(b.Objects, projection.ObjectRef{Kind: r.Kind, Name: r.Name, UID: r.UID, SpecDigest: r.SpecDigest})
		}
	}
	sort.Slice(b.Objects, func(i, j int) bool {
		return b.Objects[i].Kind+"/"+b.Objects[i].Name < b.Objects[j].Kind+"/"+b.Objects[j].Name
	})
	return b.Canonical()
}

func (p *Platform) VerifyBinding(ctx context.Context, tenant string, b cpup01.EnvironmentBindingSnapshot) error {
	row := biz.TenantExecutionEnvironment{ID: b.BindingID, TenantID: tenant, ClusterID: b.ClusterID, NamespaceName: b.NamespaceName, NamespaceUID: b.NamespaceUID}
	if b.ClusterID != p.config.ClusterID {
		return biz.EnvironmentConflict("ENVIRONMENT_CONFIG_DRIFT")
	}
	if e := p.VerifyNamespace(ctx, &row); e != nil {
		return e
	}
	projectionObject, err := p.get(ctx, resource("ConfigMap"), b.NamespaceName, "modeldev-execution-environment")
	if err != nil {
		return kubernetesFailure(err)
	}
	data, ok, _ := unstructured.NestedStringMap(projectionObject.Object, "data")
	if !ok || len(data) != 2 || data["binding.sha256"] != b.BindingDigest || biz.EnvironmentDigest([]byte(data["binding.json"])) != b.BindingDigest {
		return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
	}
	binding, err := projection.Decode([]byte(data["binding.json"]))
	if err != nil || binding.Snapshot(b.BindingDigest) != b {
		return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
	}
	refs := []any{}
	for _, r := range binding.ManagedImageAccess.PullBindings {
		refs = append(refs, map[string]any{"name": r.SecretName})
	}
	for _, identity := range []struct{ name, uid string }{{b.Identities.KFPStepServiceAccount, b.Identities.KFPStepServiceAccountUID}, {b.Identities.TrainerServiceAccount, b.Identities.TrainerServiceAccountUID}, {b.Identities.VerifierServiceAccount, b.Identities.VerifierServiceAccountUID}} {
		o, e := p.get(ctx, resource("ServiceAccount"), b.NamespaceName, identity.name)
		if e != nil {
			return originalObjectFailure(e)
		}
		if string(o.GetUID()) != identity.uid || !sameOwner(o, object(&row, "ServiceAccount", identity.name, nil)) {
			return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
		}
		got, _, _ := unstructured.NestedSlice(o.Object, "imagePullSecrets")
		if identity.name == "trainer-workload" {
			if len(got) != 0 || o.Object["automountServiceAccountToken"] != false {
				return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
			}
		} else if !reflect.DeepEqual(got, refs) {
			return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
		}
	}
	return p.VerifyNamespace(ctx, &row)
}
func (p *Platform) Publish(ctx context.Context, row *biz.TenantExecutionEnvironment, checkpoint func() error) error {
	return p.ensureObject(ctx, row, object(row, "ConfigMap", "modeldev-execution-environment", map[string]any{"immutable": true, "data": map[string]any{"binding.json": string(row.BindingCanonical), "binding.sha256": row.BindingDigest}}), checkpoint)
}
func (p *Platform) VerifyPublication(ctx context.Context, row *biz.TenantExecutionEnvironment) error {
	b, err := projection.Decode(row.BindingCanonical)
	if err != nil {
		return err
	}
	if err = p.VerifyBinding(ctx, row.TenantID, b.Snapshot(row.BindingDigest)); err != nil {
		return err
	}
	o, err := p.get(ctx, resource("ConfigMap"), row.NamespaceName, "modeldev-execution-environment")
	r := receipt(row, "ConfigMap", "modeldev-execution-environment")
	if err != nil {
		return originalObjectFailure(err)
	}
	if r == nil || r.UID != string(o.GetUID()) || !sameManaged(o, object(row, "ConfigMap", "modeldev-execution-environment", map[string]any{"immutable": true, "data": map[string]any{"binding.json": string(row.BindingCanonical), "binding.sha256": row.BindingDigest}})) {
		return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
	}
	return p.verifyBootstrap(ctx)
}
