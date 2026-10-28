package traininglogs_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/traininglogs"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// logPodPeer serves one Pod whose container status depends on the fixture. Only
// the Kubernetes API is substituted; all container selection and verification
// code runs for real.
type logPodPeer struct {
	mu       sync.Mutex
	pod      map[string]any
	logs     string
	last     map[string]string
	requests []string
}

func (p *logPodPeer) set(pod map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pod = pod
}

func (p *logPodPeer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.requests = append(p.requests, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/log"):
			query := r.URL.Query()
			p.last = map[string]string{
				"container": query.Get("container"), "previous": query.Get("previous"),
				"follow": query.Get("follow"), "timestamps": query.Get("timestamps"),
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(p.logs))
		case strings.Contains(r.URL.Path, "/pods/"):
			_ = json.NewEncoder(w).Encode(p.pod)
		case strings.HasSuffix(r.URL.Path, "/namespaces/logs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "logs", "uid": "namespace-uid"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func podFixture(containerStatuses []any, initStatuses []any, containers []string, initContainers []string) map[string]any {
	specContainers := make([]any, 0, len(containers))
	for _, name := range containers {
		specContainers = append(specContainers, map[string]any{"name": name})
	}
	specInit := make([]any, 0, len(initContainers))
	for _, name := range initContainers {
		specInit = append(specInit, map[string]any{"name": name})
	}
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "md-pod", "namespace": "logs", "uid": "pod-uid"},
		"spec":     map[string]any{"containers": specContainers, "initContainers": specInit},
		"status":   map[string]any{"containerStatuses": containerStatuses, "initContainerStatuses": initStatuses},
	}
}

func newLogReader(t *testing.T, peer *logPodPeer) *traininglogs.Reader {
	t.Helper()
	server := httptest.NewTLSServer(peer.handler(t))
	t.Cleanup(server.Close)
	client, err := coreclient.NewForConfig(&rest.Config{
		Host: server.URL, BearerToken: "synthetic-log-owner",
		TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return traininglogs.New(client)
}

func target(container string, previous bool) biz.ContainerLogTarget {
	return biz.ContainerLogTarget{Namespace: "logs", NamespaceUID: "namespace-uid", PodName: "md-pod", PodUID: "pod-uid", ContainerName: container, Previous: previous}
}

// A restarted container stays readable: RestartCount alone never denies a log.
func TestContainerLogsAllowRestartedContainerForCurrentInstance(t *testing.T) {
	peer := &logPodPeer{
		pod:  podFixture([]any{map[string]any{"name": "node", "restartCount": 3}}, nil, []string{"node"}, nil),
		logs: "2026-10-23T01:00:00Z current instance line\n",
	}
	reader := newLogReader(t, peer)
	got, err := reader.ReadContainerLogs(context.Background(), target("node", false), biz.TrainingLogOptions{TailLines: 10, MaxBytes: 1024})
	if err != nil || len(got.Lines) != 1 || got.Lines[0].Text != "current instance line" {
		t.Fatalf("restarted container current instance unavailable: %+v %v", got, err)
	}
	if peer.last["previous"] != "" || peer.last["container"] != "node" {
		t.Fatalf("current instance selection: %#v", peer.last)
	}
}

// `previous=true` reads the last terminated instance, and its absence is an
// explicit error rather than an empty successful tail.
func TestContainerLogsPreviousInstanceRequiresLastTermination(t *testing.T) {
	terminated := map[string]any{"terminated": map[string]any{"exitCode": 137, "reason": "OOMKilled"}}
	peer := &logPodPeer{
		pod:  podFixture([]any{map[string]any{"name": "node", "restartCount": 1, "lastState": terminated}}, nil, []string{"node"}, nil),
		logs: "2026-10-23T01:00:00Z previous instance line\n",
	}
	reader := newLogReader(t, peer)
	got, err := reader.ReadContainerLogs(context.Background(), target("node", true), biz.TrainingLogOptions{TailLines: 10, MaxBytes: 1024})
	if err != nil || len(got.Lines) != 1 || got.Lines[0].Text != "previous instance line" {
		t.Fatalf("previous instance unavailable: %+v %v", got, err)
	}
	if peer.last["previous"] != "true" {
		t.Fatalf("previous instance not requested: %#v", peer.last)
	}

	peer.set(podFixture([]any{map[string]any{"name": "node", "restartCount": 1}}, nil, []string{"node"}, nil))
	before := len(peer.requests)
	if _, err := reader.ReadContainerLogs(context.Background(), target("node", true), biz.TrainingLogOptions{TailLines: 10, MaxBytes: 1024}); !errors.Is(err, biz.ErrLogPreviousUnavailable) {
		t.Fatalf("missing lastState must be a distinct error: %v", err)
	}
	for _, path := range peer.requests[before:] {
		if strings.HasSuffix(path, "/log") {
			t.Fatal("a missing previous instance still reached the log endpoint")
		}
	}
}

// An init container failure is readable, but a container the Pod never declared
// cannot be used to reach another workload.
func TestContainerLogsReadInitContainerAndRejectUndeclaredName(t *testing.T) {
	peer := &logPodPeer{
		pod:  podFixture([]any{map[string]any{"name": "main", "restartCount": 0}}, []any{map[string]any{"name": "prepare-data", "restartCount": 0}}, []string{"main"}, []string{"prepare-data"}),
		logs: "2026-10-23T01:00:00Z init failure detail\n",
	}
	reader := newLogReader(t, peer)
	if got, err := reader.ReadContainerLogs(context.Background(), target("prepare-data", false), biz.TrainingLogOptions{TailLines: 10, MaxBytes: 1024}); err != nil || len(got.Lines) != 1 {
		t.Fatalf("init container log unavailable: %+v %v", got, err)
	}
	if _, err := reader.ReadContainerLogs(context.Background(), target("workspace", false), biz.TrainingLogOptions{TailLines: 10, MaxBytes: 1024}); !errors.Is(err, biz.ErrTrainingLogsUnavailable) {
		t.Fatalf("undeclared container reached the log endpoint: %v", err)
	}
}

// The follow stream re-applies the Pod identity and applies the resume offset by
// discarding the prefix; a short log means the offset is no longer valid.
func TestContainerLogStreamFollowsAndRejectsStaleOffset(t *testing.T) {
	peer := &logPodPeer{
		pod:  podFixture([]any{map[string]any{"name": "node", "restartCount": 0}}, nil, []string{"node"}, nil),
		logs: "0123456789",
	}
	reader := newLogReader(t, peer)
	stream, err := reader.StreamContainerLogs(context.Background(), target("node", false), 4)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil || string(rest) != "456789" {
		t.Fatalf("resume offset applied incorrectly: %q %v", rest, err)
	}
	if peer.last["follow"] != "true" || peer.last["timestamps"] != "true" {
		t.Fatalf("follow stream options: %#v", peer.last)
	}
	if _, err := reader.StreamContainerLogs(context.Background(), target("node", false), 64); !errors.Is(err, biz.ErrLogOffsetInvalid) {
		t.Fatalf("stale offset was accepted: %v", err)
	}
}

// A Pod UID replacement between the identity read and the stream open is refused.
func TestContainerLogStreamRejectsReplacedPodUID(t *testing.T) {
	peer := &logPodPeer{
		pod:  podFixture([]any{map[string]any{"name": "node"}}, nil, []string{"node"}, nil),
		logs: "line\n",
	}
	peer.pod["metadata"].(map[string]any)["uid"] = "replacement-uid"
	reader := newLogReader(t, peer)
	if _, err := reader.StreamContainerLogs(context.Background(), target("node", false), 0); !errors.Is(err, biz.ErrTrainingLogsUnavailable) {
		t.Fatalf("replaced Pod was streamed: %v", err)
	}
}
