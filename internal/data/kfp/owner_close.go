package kfp

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/util/validation"
)

// StopManagedRun requests termination only for the owner's frozen bound Run.
// KFP 2.16.0 run.proto defines POST /apis/v2beta1/runs/{run_id}:terminate.
// A successful response acknowledges the request, never writer absence.
func (client *Client) StopManagedRun(ctx context.Context, plan biz.PipelineDispatchPlan, authority biz.RunAuthorityCandidate) error {
	digest, err := plan.Digest()
	attempt, attemptErr := uuid.Parse(authority.AttemptID)
	if err != nil || attemptErr != nil || attempt == uuid.Nil || attempt.String() != authority.AttemptID ||
		authority.TenantID != plan.TenantID || authority.ExecutionID != plan.ExecutionID || authority.OperationID != plan.OperationID || authority.SpecHash != plan.SpecHash || authority.PlanHash != digest ||
		authority.NamespaceName != plan.Environment.NamespaceName || authority.NamespaceUID != plan.Environment.NamespaceUID || authority.NamespaceUID == "" ||
		authority.WorkflowUID == "" || len(validation.IsDNS1123Subdomain(authority.WorkflowName)) != 0 {
		return ErrManagedRunUnverified
	}
	run, err := client.GetManagedRun(ctx, plan, authority.RunID)
	if err != nil {
		return err
	}
	switch run.State {
	case "SUCCEEDED", "FAILED", "CANCELED":
		if run.FinishedAt.IsZero() || run.FinishedAt.After(time.Now()) {
			return ErrManagedRunUnverified
		}
		return nil
	case "CANCELING":
		return nil
	case "PENDING", "RUNNING", "PAUSED":
	default:
		return ErrManagedRunUnverified
	}
	if err := client.verifyMutationWorkflow(ctx, plan, authority); err != nil {
		return err
	}
	token, err := client.tokens.BearerToken(ctx, plan.TenantID, plan.Environment)
	if err != nil || len(token) > 16384 || !bearerTokenPattern.MatchString(token) || ctx.Err() != nil {
		return ErrManagedRunUnverified
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint+"/"+authority.RunID+":terminate", nil)
	if err != nil {
		return ErrManagedRunUnverified
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return biz.ErrRuntimeNotReady
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" {
		return biz.ErrRuntimeNotReady
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCreateRunResponseBytes+1))
	var empty map[string]json.RawMessage
	if err != nil || len(body) > maxCreateRunResponseBytes || json.Unmarshal(body, &empty) != nil || empty == nil || len(empty) != 0 {
		return biz.ErrRuntimeNotReady
	}
	return nil
}
