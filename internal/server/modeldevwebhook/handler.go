package modeldevwebhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sync/atomic"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Handler struct {
	reader                                        IdentityReader
	bindings                                      map[string]NamespaceBinding
	profiles                                      map[string]frozenProfile
	logger                                        *slog.Logger
	maxBody                                       int64
	requestTimeout, dependencyTimeout             time.Duration
	capacity                                      chan struct{}
	requests, denied, mutated, invalid, saturated atomic.Uint64
	certificateReady                              func() bool
}

// New freezes and validates operator configuration, including exact canonical
// Release bytes. The exported HTTP handler can be mounted on a real TLS server
// or a TLS integration fixture without replacing any admission logic.
func New(config Config, reader IdentityReader, logger *slog.Logger) (*Handler, error) {
	if reader == nil {
		return nil, ErrConfiguration
	}
	profiles, err := config.freeze()
	if err != nil {
		return nil, err
	}
	config.defaults()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	handler := &Handler{reader: reader, profiles: profiles, bindings: make(map[string]NamespaceBinding), logger: logger,
		maxBody: config.MaxBodyBytes, requestTimeout: time.Duration(config.RequestTimeoutMilliseconds) * time.Millisecond,
		dependencyTimeout: time.Duration(config.DependencyTimeoutMilliseconds) * time.Millisecond, capacity: make(chan struct{}, config.MaxConcurrentRequests)}
	for _, binding := range config.Bindings {
		binding.Profiles = append([]string(nil), binding.Profiles...)
		if binding.Driver != nil {
			driver := *binding.Driver
			driver.Command = append([]string(nil), driver.Command...)
			driver.AllowedSupportImages = append([]string(nil), driver.AllowedSupportImages...)
			binding.Driver = &driver
		}
		handler.bindings[binding.Namespace] = binding
	}
	return handler, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	switch request.URL.Path {
	case "/livez":
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusOK)
	case "/readyz":
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), handler.dependencyTimeout)
		defer cancel()
		if !handler.ready(ctx) {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	case "/metrics":
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(writer, "modeldev_webhook_requests_total %d\nmodeldev_webhook_denied_total %d\nmodeldev_webhook_mutated_total %d\nmodeldev_webhook_invalid_total %d\nmodeldev_webhook_saturated_total %d\nmodeldev_webhook_inflight %d\n", handler.requests.Load(), handler.denied.Load(), handler.mutated.Load(), handler.invalid.Load(), handler.saturated.Load(), len(handler.capacity))
	case "/mutate":
		handler.mutate(writer, request)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (handler *Handler) ready(ctx context.Context) bool {
	if handler.certificateReady != nil && !handler.certificateReady() {
		return false
	}
	for _, binding := range handler.bindings {
		namespace, err := handler.reader.Namespace(ctx, binding.Namespace)
		if err != nil || namespace == nil || namespace.Name != binding.Namespace || string(namespace.UID) != binding.NamespaceUID || namespace.DeletionTimestamp != nil || namespace.Labels[binding.OwnerLabel.Key] != binding.OwnerLabel.Value {
			return false
		}
	}
	return ctx.Err() == nil
}

func (handler *Handler) mutate(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writer.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	select {
	case handler.capacity <- struct{}{}:
		defer func() { <-handler.capacity }()
	default:
		handler.saturated.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	handler.requests.Add(1)
	ctx, cancel := context.WithTimeout(request.Context(), handler.requestTimeout)
	defer cancel()
	controller := http.NewResponseController(writer)
	_ = controller.SetReadDeadline(time.Now().Add(handler.requestTimeout))
	_ = controller.SetWriteDeadline(time.Now().Add(handler.requestTimeout))
	request.Body = http.MaxBytesReader(writer, request.Body, handler.maxBody)
	decoder := json.NewDecoder(request.Body)
	var review admissionv1.AdmissionReview
	if decoder.Decode(&review) != nil || decoder.Decode(new(any)) != io.EOF || review.APIVersion != "admission.k8s.io/v1" || review.Kind != "AdmissionReview" || review.Request == nil || !canonicalUUID(string(review.Request.UID)) {
		handler.invalid.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	response, decision := handler.admit(ctx, review.Request)
	if ctx.Err() != nil {
		response, decision = denied(review.Request, "dependency_unavailable")
	}
	if !response.Allowed {
		handler.denied.Add(1)
	}
	if decision == "mutated" || decision == "driver_retained" {
		handler.mutated.Add(1)
	}
	// Deliberately omit UID, namespace, object, token, API errors and body.
	handler.logger.Info("modeldev pod admission", "decision", decision, "allowed", response.Allowed)
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(admissionv1.AdmissionReview{TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Response: response})
}

var _ http.Handler = (*Handler)(nil)
