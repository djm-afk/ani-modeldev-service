package main

import (
	"context"
	"encoding/hex"
	"flag"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"google.golang.org/protobuf/encoding/protojson"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type invocation struct {
	step           string
	config         ownerConfig
	claim          *modeldevv1.StepContext
	publicationRef *string
	closeOnly      bool
	kube           dynamic.Interface
}

// bootstrapInvocation is the CLI input boundary used before any business RPC.
// A nil client selects the official in-cluster client. The resulting association
// is a claim only; the server verifies TokenReview and actual KFP task membership.
func bootstrapInvocation(ctx context.Context, args []string, kube dynamic.Interface) (invocation, error) {
	invalid := component.ErrConfiguration
	if ctx == nil || len(args) == 0 {
		return invocation{}, invalid
	}
	step := args[0]
	stepValue, ok := map[string]modeldevv1.PipelineStep{
		"prepare":    modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE,
		"train-wait": modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT,
		"collect":    modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT,
		"publish":    modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH,
		"close":      modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE,
		"export":     modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT,
		"verify":     modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY,
		"register":   modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER,
	}[step]
	if !ok {
		return invocation{}, invalid
	}
	flags := flag.NewFlagSet("ani-modeldev-step", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configFile := flags.String("config", "", "owner component configuration file")
	executionID := flags.String("execution-id", "", "frozen execution identity")
	specHash := flags.String("spec-hash", "", "frozen execution spec hash")
	runID := flags.String("run-id", "", "actual KFP run identity")
	pvcName := flags.String("pvc-name", "", "allocated workspace PVC candidate")
	referenceFile := flags.String("publication-ref-file", "", "KFP small publication reference output file")
	reference := flags.String("publication-ref", "", "KFP small publication reference input parameter")
	closeOnly := flags.Bool("close-only", false, "fence and observe close without publication proof")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *configFile == "" {
		return invocation{}, invalid
	}
	provided := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if provided["close-only"] && (step != "close" || (*closeOnly && (provided["publication-ref-file"] || provided["publication-ref"]))) {
		return invocation{}, invalid
	}
	var config ownerConfig
	if err := readConfig(*configFile, &config); err != nil {
		return invocation{}, err
	}
	if config.TenantID == "" || config.ServerName == "" || strings.Contains(config.Target, "://") {
		return invocation{}, invalid
	}
	if host, port, err := net.SplitHostPort(config.Target); err != nil || host == "" || port == "" {
		return invocation{}, invalid
	}
	if config.TimeoutSeconds <= 0 || config.TimeoutSeconds > 86400 || config.PollIntervalSeconds < 1 || config.PollIntervalSeconds > 30 {
		return invocation{}, invalid
	}
	if provided["publication-ref-file"] && provided["publication-ref"] {
		return invocation{}, invalid
	}
	if provided["publication-ref-file"] {
		if step != "publish" {
			return invocation{}, invalid
		}
		config.PublicationRefFile = *referenceFile
	}
	var candidate *string
	if provided["publication-ref"] {
		if step != "close" && step != "register" {
			return invocation{}, invalid
		}
		if *reference != "" || step != "close" {
			if _, err := cpup01.ParsePublicationRef([]byte(*reference)); err != nil {
				return invocation{}, invalid
			}
		}
		config.PublicationRef = *reference
		candidate = reference
	}
	if provided["pvc-name"] {
		if step != "prepare" || *pvcName == "" || len(validation.IsDNS1123Subdomain(*pvcName)) != 0 {
			return invocation{}, invalid
		}
		config.PVCName = *pvcName
	}
	dynamicIdentity := provided["execution-id"] || provided["spec-hash"] || provided["run-id"]
	if dynamicIdentity && config.ContextFile != "" {
		return invocation{}, invalid
	}
	claim := &modeldevv1.StepContext{}
	if !dynamicIdentity {
		data, err := readOwnerFile(config.ContextFile, 64<<10)
		if err != nil {
			return invocation{}, err
		}
		if protojson.Unmarshal(data, claim) != nil {
			return invocation{}, invalid
		}
		if claim.Step != stepValue {
			return invocation{}, invalid
		}
		return invocation{step: step, config: config, claim: claim, publicationRef: candidate, closeOnly: *closeOnly, kube: kube}, nil
	}
	if !canonicalUUID(config.TenantID) || !canonicalUUID(config.NamespaceUID) || !canonicalUUID(*executionID) || !canonicalUUID(*runID) || len(*specHash) != 64 || strings.ToLower(*specHash) != *specHash {
		return invocation{}, invalid
	}
	if _, err := hex.DecodeString(*specHash); err != nil {
		return invocation{}, invalid
	}
	podName, podUID, namespace := os.Getenv("ANI_POD_NAME"), os.Getenv("ANI_POD_UID"), os.Getenv("ANI_POD_NAMESPACE")
	if podName == "" || namespace == "" || len(validation.IsDNS1123Subdomain(podName)) != 0 || len(validation.IsDNS1123Label(namespace)) != 0 || !canonicalUUID(podUID) {
		return invocation{}, invalid
	}
	if kube == nil {
		var err error
		kube, err = inClusterKubernetes()
		if err != nil {
			return invocation{}, err
		}
	}
	pod, err := kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil || pod == nil || pod.GetAPIVersion() != "v1" || pod.GetKind() != "Pod" || pod.GetName() != podName || pod.GetNamespace() != namespace || string(pod.GetUID()) != podUID || pod.GetDeletionTimestamp() != nil {
		return invocation{}, invalid
	}
	var controller *metav1.OwnerReference
	for _, owner := range pod.GetOwnerReferences() {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		if controller != nil {
			return invocation{}, invalid
		}
		copy := owner
		controller = &copy
	}
	if controller == nil || controller.APIVersion != "argoproj.io/v1alpha1" || controller.Kind != "Workflow" || controller.Name == "" || len(validation.IsDNS1123Subdomain(controller.Name)) != 0 || !canonicalUUID(string(controller.UID)) {
		return invocation{}, invalid
	}
	claim = &modeldevv1.StepContext{
		Identity:    &trainingv1.ExecutionIdentity{ExecutionId: *executionID, ExecutionSpecHash: *specHash},
		Association: &modeldevv1.RunAssociation{KfpRunId: *runID, NamespaceName: namespace, NamespaceUid: config.NamespaceUID, WorkflowName: controller.Name, WorkflowUid: string(controller.UID), PodName: podName, PodUid: podUID},
		Step:        stepValue,
	}
	return invocation{step: step, config: config, claim: claim, publicationRef: candidate, closeOnly: *closeOnly, kube: kube}, nil
}

func canonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func inClusterKubernetes() (dynamic.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, component.ErrConfiguration
	}
	config.Timeout = 15 * time.Second
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, component.ErrConfiguration
	}
	return client, nil
}
