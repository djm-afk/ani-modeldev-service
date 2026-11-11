package modeldevwebhook_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const (
	importerTenant  = "64c5c35a-f554-432c-b4bc-7b4f36f08420"
	importerDataset = "3bd411c0-3923-4d6a-a9f0-5b7b3e97b3ed"
	importerTask    = "20eac648-8d3a-4174-8908-0311bc288c1b"
	importerAttempt = "fbd73f1f-f2e3-4cec-b2e3-80d61e645ada"
	importerJobUID  = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	importerNSUID   = "a8d8a472-8a8a-43cf-abf0-0e805bbe67b0"
	// testImporterImage is the frozen importer image every admitted importer Pod
	// must use.
	testImporterImage = "example.invalid/importer@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// importerPod builds the Pod the control plane creates for one attempt.
func importerPod(mutate func(*corev1.Pod)) *corev1.Pod {
	noToken := false
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "md-import-job-abcde", Namespace: "tenant-" + importerTenant,
			Labels: map[string]string{
				"ani.io/managed-by":              "ani-modeldev-service",
				"modeldev.ani.io/purpose":        "dataset-import",
				"modeldev.ani.io/tenant-id":      importerTenant,
				"modeldev.ani.io/dataset-id":     importerDataset,
				"modeldev.ani.io/task-id":        importerTask,
				"modeldev.ani.io/attempt-id":     importerAttempt,
				"modeldev.ani.io/environment-id": "fa7f113c-58c1-4692-a9e2-e9687e8dd162",
			},
			Annotations: map[string]string{"modeldev.ani.io/namespace-uid": importerNSUID},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: "md-import-job",
				UID: types.UID(importerJobUID), Controller: func() *bool { v := true; return &v }(),
			}},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName:           "modeldev-dataset-importer",
			AutomountServiceAccountToken: &noToken,
			RestartPolicy:                corev1.RestartPolicyNever,
			Containers:                   []corev1.Container{{Name: "importer", Image: testImporterImage}},
		},
	}
	if mutate != nil {
		mutate(pod)
	}
	return pod
}

func importerJob() *batchv1.Job {
	noToken := false
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "md-import-job", Namespace: "tenant-" + importerTenant,
			UID: types.UID(importerJobUID),
			Labels: map[string]string{
				"modeldev.ani.io/purpose":    "dataset-import",
				"modeldev.ani.io/tenant-id":  importerTenant,
				"modeldev.ani.io/dataset-id": importerDataset,
				"modeldev.ani.io/task-id":    importerTask,
				"modeldev.ani.io/attempt-id": importerAttempt,
			}},
		Spec: batchv1.JobSpec{
			Suspend: func() *bool { v := false; return &v }(),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				ServiceAccountName:           "modeldev-dataset-importer",
				AutomountServiceAccountToken: &noToken,
			}},
		},
	}
}

func importerReview(pod *corev1.Pod) admissionv1.AdmissionReview {
	raw, _ := json.Marshal(pod)
	return admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID: types.UID("11111111-1111-4111-8111-111111111111"), Kind: metav1.GroupVersionKind{Version: "v1", Kind: "Pod"},
			Resource:  metav1.GroupVersionResource{Version: "v1", Resource: "pods"},
			Operation: admissionv1.Create, Namespace: pod.Namespace,
			UserInfo: authenticationv1.UserInfo{Username: "system:serviceaccount:kube-system:job-controller"},
			Object:   runtime.RawExtension{Raw: raw},
		},
	}
}

// TestImporterPodIsAdmittedWithoutATrainingEnvironment is the regression for the
// blocking defect: the webhook resolved the training environment for every
// protected Pod, so every legitimate importer Pod was refused with
// environment_identity even though it has no training Binding by design.
func TestImporterPodIsAdmittedWithoutATrainingEnvironment(t *testing.T) {
	config, reader := fixtureConfig(t)
	reader.job = importerJob()
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	result := call(t, server, importerReview(importerPod(nil)))
	if !result.Allowed {
		t.Fatalf("importer Pod refused: %s", result.Result.Message)
	}
}

// TestImporterPodCannotBorrowTheIdentity covers the negative cases the branch
// exists to keep closed.
func TestImporterPodCannotBorrowTheIdentity(t *testing.T) {
	cases := map[string]struct {
		pod  func(*corev1.Pod)
		job  func(*batchv1.Job)
		want string
	}{
		// Without the purpose label the Pod is not an importer at all, so it
		// falls through to the training branch and is refused there. The point
		// is that dropping the label cannot reach the importer path.
		"missing purpose label": {
			pod:  func(p *corev1.Pod) { delete(p.Labels, "modeldev.ani.io/purpose") },
			want: "environment_identity",
		},
		"wrong namespace for tenant": {
			pod:  func(p *corev1.Pod) { p.Namespace = "tenant-99999999-9999-4999-8999-999999999999" },
			want: "importer_labels",
		},
		"extra container": {
			pod: func(p *corev1.Pod) {
				p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "shell"})
			},
			want: "container_contract",
		},
		"token automounted": {
			pod:  func(p *corev1.Pod) { v := true; p.Spec.AutomountServiceAccountToken = &v },
			want: "container_contract",
		},
		"wrong image": {
			pod: func(p *corev1.Pod) {
				p.Spec.Containers[0].Image = "example.invalid/importer@sha256:" + strings.Repeat("d", 64)
			},
			want: "image_contract",
		},
		"job carries a different attempt": {
			job:  func(j *batchv1.Job) { j.Labels["modeldev.ani.io/attempt-id"] = "11111111-2222-4333-8444-555555555555" },
			want: "job_identity",
		},
		"job suspended": {
			job:  func(j *batchv1.Job) { v := true; j.Spec.Suspend = &v },
			want: "job_suspended",
		},
		"job belongs to another identity": {
			job:  func(j *batchv1.Job) { j.UID = types.UID("bbbbbbbb-cccc-4ddd-8eee-ffffffffffff") },
			want: "job_identity",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			config, reader := fixtureConfig(t)
			job := importerJob()
			if tc.job != nil {
				tc.job(job)
			}
			reader.job = job
			handler, err := modeldevwebhook.New(config, reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(handler)
			defer server.Close()

			result := call(t, server, importerReview(importerPod(tc.pod)))
			if result.Allowed {
				t.Fatal("borrowed importer identity was admitted")
			}
			if result.Result == nil || result.Result.Message != "ModelDev Pod admission refused: "+tc.want {
				t.Fatalf("unexpected refusal: %+v", result.Result)
			}
		})
	}
}
