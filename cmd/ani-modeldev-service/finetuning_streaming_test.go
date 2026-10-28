package main

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestFinetuningStreamingRuntimeStoragePreservesAPIBudget(t *testing.T) {
	f := runtimeAppFixture(t)
	cancelled := make(chan struct{}, 1)
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		_, _ = io.WriteString(w, "x")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer peer.Close()
	ca := filepath.Join(t.TempDir(), "storage-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	f.config.Runtime.ObjectStorage.Endpoint = peer.URL
	f.config.Runtime.ObjectStorage.CaFile = ca
	for _, streaming := range []time.Duration{0, 600 * time.Second} {
		f.config.Runtime.ObjectStorage.StreamingTimeout = nil
		if streaming != 0 {
			f.config.Runtime.ObjectStorage.StreamingTimeout = durationpb.New(streaming)
		}
		if err := f.config.Validate(); err != nil {
			t.Fatal(err)
		}
		clients, err := loadRuntimeClients(f.config.Runtime, f.config.ImageRuntime)
		if err != nil {
			t.Fatal(err)
		}
		defer clients.close()
		httpClient := clients.store.Options().HTTPClient.(*http.Client)
		want := streaming
		if want == 0 {
			want = f.config.Runtime.ApiTimeout.AsDuration()
		}
		if httpClient.Timeout != want || httpClient.Transport.(*http.Transport).ResponseHeaderTimeout != 3*time.Second || f.config.Runtime.ApiTimeout.AsDuration() != 3*time.Second {
			t.Fatalf("storage override changed original API/header budgets: %+v", httpClient)
		}
		if streaming == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		started := time.Now()
		out, err := clients.store.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("synthetic-bucket"), Key: aws.String("stream")})
		if err != nil {
			cancel()
			t.Fatalf("real HTTPS headers did not arrive: %v", err)
		}
		body, readErr := io.ReadAll(out.Body)
		_ = out.Body.Close()
		cancel()
		if string(body) != "x" || !errors.Is(readErr, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
			t.Fatalf("parent deadline did not bound actual streaming response: body=%q error=%v elapsed=%s", body, readErr, time.Since(started))
		}
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("HTTPS server did not observe request cancellation")
		}
		t.Logf("backend storage timeout=%s API/header timeout=3s parent deadline=300ms actual cancellation elapsed=%s", httpClient.Timeout, time.Since(started))
	}
}
