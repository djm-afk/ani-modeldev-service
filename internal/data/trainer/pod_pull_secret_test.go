package trainer_test

import (
	"path"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	corev1 "k8s.io/api/core/v1"
)

// The API server copies a ServiceAccount's imagePullSecrets onto any Pod that
// declares none, so the finetuning worker always carries the tenant registry
// credential its frozen template omits. That inheritance must not read as a
// specification change, while a substituted or injected reference still must.
func TestValidatePodTemplateAcceptsServiceAccountPullSecretInheritance(t *testing.T) {
	shared := corev1.LocalObjectReference{Name: "ani-pull-" + strings.Repeat("a", 32)}
	inherited := corev1.LocalObjectReference{Name: "ani-registry-pull"}

	cases := []struct {
		name     string
		expected []corev1.LocalObjectReference
		actual   []corev1.LocalObjectReference
		accepted bool
	}{
		{"training references the frozen Secret", []corev1.LocalObjectReference{shared}, []corev1.LocalObjectReference{shared}, true},
		{"training Secret replaced", []corev1.LocalObjectReference{shared}, []corev1.LocalObjectReference{{Name: "ani-pull-" + strings.Repeat("b", 32)}}, false},
		{"training Secret dropped", []corev1.LocalObjectReference{shared}, nil, false},
		{"finetuning inherits the ServiceAccount credential", nil, []corev1.LocalObjectReference{inherited}, true},
		{"finetuning inherits nothing", nil, nil, true},
		{"finetuning gains a private registry credential", nil, []corev1.LocalObjectReference{{Name: "attacker-registry"}}, false},
		{"finetuning inherits plus an injected credential", nil, []corev1.LocalObjectReference{inherited, {Name: "attacker-registry"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := minimalWorkerPodSpec()
			expected.ImagePullSecrets = tc.expected
			actual := minimalWorkerPodSpec()
			actual.ImagePullSecrets = tc.actual
			err := trainer.ValidatePodTemplate(expected, actual)
			if tc.accepted && err != nil {
				t.Fatalf("expected the Pod to be accepted, got %v", err)
			}
			if !tc.accepted && err == nil {
				t.Fatal("expected the Pod to be rejected")
			}
		})
	}
}

// minimalWorkerPodSpec is the smallest single-worker Pod that satisfies every
// other clause of the contract, so a failure can only come from the pull
// Secret comparison under test.
func minimalWorkerPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		ServiceAccountName:           "trainer-workload",
		SchedulerName:                "volcano",
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: boolPointer(false),
		Containers: []corev1.Container{{
			Name:    "node",
			Image:   "registry.example.test/train@sha256:" + strings.Repeat("c", 64),
			Command: []string{path.Join("/opt/ani-modeldev/training", "train_mlp.py")},
			Args:    []string{"--device", "cpu"},
		}},
	}
}

func boolPointer(value bool) *bool { return &value }
