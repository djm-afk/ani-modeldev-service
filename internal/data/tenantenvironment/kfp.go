package tenantenvironment

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

type PlatformTokenSource struct{ File string }

func (s PlatformTokenSource) Token(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	raw, err := readFile(s.File, 16<<10)
	if err != nil || len(raw) == 0 || !tokenPattern.Match(raw) {
		return "", biz.EnvironmentError("KFP_AUTHENTICATION_FAILED")
	}
	return string(raw), nil
}

type experimentClient struct {
	endpoint string
	token    PlatformTokenSource
	http     *http.Client
}

func newExperimentClient(c Config, roots *x509.CertPool) (*experimentClient, error) {
	u, e := url.Parse(c.KFPEndpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSClientConfig: &tls.Config{RootCAs: roots.Clone(), MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, DisableCompression: true, MaxConnsPerHost: 4}
	return &experimentClient{endpoint: strings.TrimSuffix(c.KFPEndpoint, "/"), token: PlatformTokenSource{File: c.KFPTokenFile}, http: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (k *experimentClient) request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	token, e := k.token.Token(ctx)
	if e != nil {
		return nil, 0, e
	}
	request, e := http.NewRequestWithContext(ctx, method, k.endpoint+path, bytes.NewReader(body))
	if e != nil {
		return nil, 0, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	request.GetBody = nil
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, e := k.http.Do(request)
	if e != nil {
		return nil, 0, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return nil, response.StatusCode, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	switch response.StatusCode {
	case 401:
		return nil, 401, biz.EnvironmentError("KFP_AUTHENTICATION_FAILED")
	case 403:
		return nil, 403, biz.EnvironmentError("KFP_PERMISSION_DENIED")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	return raw, response.StatusCode, nil
}

type experiment struct {
	ID           string          `json:"experiment_id"`
	Name         string          `json:"display_name"`
	Description  string          `json:"description"`
	Namespace    string          `json:"namespace"`
	StorageState string          `json:"storage_state"`
	Error        json.RawMessage `json:"error,omitempty"`
}

func desiredIntent(row *biz.TenantExecutionEnvironment) *biz.EnvironmentExperimentIntent {
	description, _ := json.Marshal(struct {
		Schema        string `json:"s"`
		Owner         string `json:"o"`
		EnvironmentID string `json:"e"`
		TenantID      string `json:"t"`
		ClusterID     string `json:"c"`
		NamespaceUID  string `json:"n"`
	}{"ani.modeldev.tenant-environment.v1", "ani-modeldev-service", row.ID, row.TenantID, row.ClusterID, row.NamespaceUID})
	i := &biz.EnvironmentExperimentIntent{Namespace: row.NamespaceName, Name: row.ExperimentName, Description: string(description)}
	body, _ := experimentBody(i)
	i.BodyDigest = biz.EnvironmentDigest(body)
	return i
}
func experimentBody(i *biz.EnvironmentExperimentIntent) ([]byte, error) {
	return json.Marshal(struct {
		Name        string `json:"display_name"`
		Description string `json:"description"`
		Namespace   string `json:"namespace"`
	}{i.Name, i.Description, i.Namespace})
}
func matchesExperiment(e experiment, i *biz.EnvironmentExperimentIntent) bool {
	return biz.CanonicalEnvironmentUUID(e.ID) && e.Name == i.Name && e.Namespace == i.Namespace && e.Description == i.Description && e.StorageState == "AVAILABLE" && len(e.Error) == 0
}
func (k *experimentClient) get(ctx context.Context, id string) (experiment, int, error) {
	raw, code, e := k.request(ctx, http.MethodGet, "/apis/v2beta1/experiments/"+url.PathEscape(id), nil)
	var result experiment
	if e != nil {
		return result, code, e
	}
	if json.Unmarshal(raw, &result) != nil {
		return result, code, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	return result, code, nil
}
func (k *experimentClient) find(ctx context.Context, i *biz.EnvironmentExperimentIntent) ([]experiment, error) {
	token := ""
	seen := map[string]bool{}
	matches := []experiment{}
	for page := 0; page < 10; page++ {
		filter, _ := json.Marshal(map[string]any{"predicates": []any{map[string]any{"key": "display_name", "operation": "EQUALS", "string_value": i.Name}}})
		q := url.Values{"namespace": {i.Namespace}, "page_size": {"100"}, "filter": {string(filter)}}
		if token != "" {
			q.Set("page_token", token)
		}
		raw, _, e := k.request(ctx, http.MethodGet, "/apis/v2beta1/experiments?"+q.Encode(), nil)
		if e != nil {
			return nil, e
		}
		var list struct {
			Experiments   []experiment `json:"experiments"`
			NextPageToken string       `json:"next_page_token"`
		}
		if json.Unmarshal(raw, &list) != nil || len(list.Experiments) > 100 {
			return nil, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
		}
		for _, item := range list.Experiments {
			if item.Name == i.Name && item.Namespace == i.Namespace {
				matches = append(matches, item)
			}
		}
		token = list.NextPageToken
		if token == "" {
			return matches, nil
		}
		if seen[token] {
			return nil, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
		}
		seen[token] = true
	}
	return nil, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
}
func (p *Platform) Experiment(ctx context.Context, row *biz.TenantExecutionEnvironment, checkpoint func() error) error {
	wanted := desiredIntent(row)
	if row.ExperimentIntent == nil {
		row.ExperimentIntent = wanted
		if e := checkpoint(); e != nil {
			return e
		}
	} else if row.ExperimentIntent.Namespace != wanted.Namespace || row.ExperimentIntent.Name != wanted.Name || row.ExperimentIntent.Description != wanted.Description || row.ExperimentIntent.BodyDigest != wanted.BodyDigest {
		return biz.EnvironmentConflict("KFP_EXPERIMENT_CONFLICT")
	}
	intent := row.ExperimentIntent
	if row.ExperimentID != "" {
		e, code, err := p.kfp.get(ctx, row.ExperimentID)
		if code == 404 {
			return biz.EnvironmentConflict("KFP_EXPERIMENT_CONFLICT")
		}
		if err != nil {
			return err
		}
		if !matchesExperiment(e, intent) {
			return biz.EnvironmentConflict("KFP_EXPERIMENT_CONFLICT")
		}
		return nil
	}
	matches, err := p.kfp.find(ctx, intent)
	if err != nil {
		return err
	}
	if len(matches) > 1 {
		return biz.EnvironmentConflict("KFP_EXPERIMENT_CONFLICT")
	}
	if len(matches) == 1 {
		if !matchesExperiment(matches[0], intent) {
			return biz.EnvironmentConflict("KFP_EXPERIMENT_CONFLICT")
		}
		row.ExperimentID = matches[0].ID
		confirmed, _, e := p.kfp.get(ctx, row.ExperimentID)
		if e != nil {
			return e
		}
		if !matchesExperiment(confirmed, intent) {
			return biz.EnvironmentConflict("KFP_EXPERIMENT_CONFLICT")
		}
		return checkpoint()
	}
	if intent.Sent && !p.config.ExperimentUniquenessVerified {
		return biz.EnvironmentUncertain()
	}
	if err = p.VerifyNamespace(ctx, row); err != nil {
		return err
	}
	intent.Sent = true
	if err = checkpoint(); err != nil {
		return err
	}
	body, _ := experimentBody(intent)
	raw, postCode, postErr := p.kfp.request(ctx, http.MethodPost, "/apis/v2beta1/experiments", body)
	if postCode == 401 || postCode == 403 {
		intent.Sent = false
		if e := checkpoint(); e != nil {
			return e
		}
		return postErr
	}
	if postErr == nil {
		var result experiment
		if json.Unmarshal(raw, &result) == nil && matchesExperiment(result, intent) {
			confirmed, _, e := p.kfp.get(ctx, result.ID)
			if e == nil && matchesExperiment(confirmed, intent) {
				row.ExperimentID = result.ID
				return checkpoint()
			}
		}
	}
	// A lost response never authorizes a second POST in this Ensure call.
	matches, err = p.kfp.find(ctx, intent)
	if err == nil && len(matches) == 1 && matchesExperiment(matches[0], intent) {
		confirmed, _, e := p.kfp.get(ctx, matches[0].ID)
		if e == nil && matchesExperiment(confirmed, intent) {
			row.ExperimentID = confirmed.ID
			return checkpoint()
		}
	}
	if len(matches) > 1 {
		return biz.EnvironmentConflict("KFP_EXPERIMENT_CONFLICT")
	}
	return biz.EnvironmentUncertain()
}
func (p *Platform) Verify(ctx context.Context, row *biz.TenantExecutionEnvironment, checkpoint func() error) error {
	if err := p.VerifyNamespace(ctx, row); err != nil {
		return err
	}
	objects, err := p.publicObjects(row)
	if err != nil {
		return err
	}
	for _, expected := range objects {
		actual, e := p.get(ctx, resource(expected.GetKind()), row.NamespaceName, expected.GetName())
		r := receipt(row, expected.GetKind(), expected.GetName())
		if e != nil {
			return originalObjectFailure(e)
		}
		if r == nil || r.UID != string(actual.GetUID()) || !sameManaged(actual, expected) {
			return biz.EnvironmentConflict("ENVIRONMENT_OBJECT_CONFLICT")
		}
		r.CheckedAt = time.Now().UTC()
		if e = checkpoint(); e != nil {
			return e
		}
	}
	if err = p.Experiment(ctx, row, checkpoint); err != nil {
		return err
	}
	if err = p.verifyBootstrap(ctx); err != nil {
		return err
	}
	for _, profile := range p.config.Profiles {
		confirmed := false
		for _, r := range row.Receipts.Materials {
			if r.Kind == "PipelineVersion" && r.ID == profile.VersionID && r.Parent == profile.PipelineID && r.SpecDigest == profile.IRSHA256 {
				confirmed = true
			}
		}
		if confirmed && row.ReadyAt.IsZero() {
			continue
		}
		raw, _, e := p.kfp.request(ctx, http.MethodGet, "/apis/v2beta1/pipelines/"+profile.PipelineID, nil)
		if e != nil {
			return e
		}
		var pipeline struct {
			ID        string `json:"pipeline_id"`
			Name      string `json:"display_name"`
			Namespace string `json:"namespace"`
		}
		if json.Unmarshal(raw, &pipeline) != nil || pipeline.ID != profile.PipelineID || pipeline.Namespace != "ani-system" || pipeline.Name != profile.ID {
			return biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
		}
		raw, _, e = p.kfp.request(ctx, http.MethodGet, "/apis/v2beta1/pipelines/"+profile.PipelineID+"/versions/"+profile.VersionID, nil)
		if e != nil {
			return e
		}
		var version struct {
			ID           string          `json:"pipeline_version_id"`
			PipelineID   string          `json:"pipeline_id"`
			Name         string          `json:"display_name"`
			PipelineSpec json.RawMessage `json:"pipeline_spec"`
		}
		if json.Unmarshal(raw, &version) != nil || version.ID != profile.VersionID || version.PipelineID != profile.PipelineID || version.Name != "ir-"+profile.IRSHA256[:16] {
			return biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
		}
		var spec any
		expected, e := p.readIR(profile.ID, profile.IRSHA256)
		if e != nil || json.Unmarshal(version.PipelineSpec, &spec) != nil || !reflect.DeepEqual(spec, expected) {
			return biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
		}
		if e = p.verifyFixedImages(spec); e != nil {
			return e
		}
		found := false
		for index := range row.Receipts.Materials {
			r := &row.Receipts.Materials[index]
			if r.Kind == "PipelineVersion" && r.ID == profile.VersionID {
				if r.Parent != profile.PipelineID || r.SpecDigest != profile.IRSHA256 {
					return biz.EnvironmentConflict("ENVIRONMENT_CONFIG_DRIFT")
				}
				r.CheckedAt = time.Now().UTC()
				found = true
			}
		}
		if !found {
			row.Receipts.Materials = append(row.Receipts.Materials, biz.EnvironmentMaterialReceipt{Kind: "PipelineVersion", ID: profile.VersionID, Parent: profile.PipelineID, SpecDigest: profile.IRSHA256, CheckedAt: time.Now().UTC()})
		}
		if e = checkpoint(); e != nil {
			return e
		}
	}
	return nil
}

func (p *Platform) readIR(profileID, digest string) (any, error) {
	file := ""
	for _, source := range p.config.IRSources {
		if source.ProfileID == profileID && source.SHA256 == digest {
			file = source.File
		}
	}
	if file == "" {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	raw, err := readRegularFile(file, 1<<20)
	if err != nil || biz.EnvironmentDigest(raw) != digest {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	first, err := reader.Read()
	if err != nil {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	encoded, err := yaml.YAMLToJSONStrict(first)
	if err != nil {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	var spec map[string]any
	if json.Unmarshal(encoded, &spec) != nil || len(spec) == 0 {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	second, err := reader.Read()
	if err == io.EOF {
		return spec, nil
	}
	if err != nil {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	encoded, err = yaml.YAMLToJSONStrict(second)
	var platform map[string]any
	if err != nil || json.Unmarshal(encoded, &platform) != nil || len(platform) == 0 {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	if _, err = reader.Read(); err != io.EOF {
		return nil, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
	}
	return map[string]any{"pipeline_spec": spec, "platform_spec": platform}, nil
}

func (p *Platform) verifyFixedImages(value any) error {
	return p.verifyFixedImageNode(value, nil)
}

func (p *Platform) verifyFixedImageNode(value any, path []string) error {
	switch v := value.(type) {
	case map[string]any:
		// KFP recognizes this exact SDK resource executor as a PVC operation;
		// argostub/createpvc is a backend marker, not a runtime container image.
		if (reflect.DeepEqual(path, []string{"deploymentSpec", "executors", "exec-createpvc"}) ||
			reflect.DeepEqual(path, []string{"pipeline_spec", "deploymentSpec", "executors", "exec-createpvc"})) &&
			reflect.DeepEqual(v, map[string]any{"container": map[string]any{"image": "argostub/createpvc"}}) {
			return nil
		}
		for key, child := range v {
			if key == "image" {
				image, ok := child.(string)
				if !ok || !strings.Contains(image, "@sha256:") || len(image[strings.LastIndex(image, "@sha256:")+8:]) != 64 {
					return biz.EnvironmentError("MANAGED_IMAGE_REGISTRY_MISMATCH")
				}
				anonymous := false
				for _, approved := range p.config.AnonymousImages {
					if approved.Reference == image && validDigest(approved.EvidenceSHA256) {
						anonymous = true
					}
				}
				if !anonymous {
					prefix := p.config.RegistryAuthority + "/" + p.config.PlatformProject + "/"
					if !strings.HasPrefix(image, prefix) {
						return biz.EnvironmentError("MANAGED_IMAGE_REGISTRY_MISMATCH")
					}
				}
			}
			if e := p.verifyFixedImageNode(child, append(path, key)); e != nil {
				return e
			}
		}
	case []any:
		for _, child := range v {
			if e := p.verifyFixedImageNode(child, append(path, "")); e != nil {
				return e
			}
		}
	}
	return nil
}
