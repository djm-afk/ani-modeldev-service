package main

import (
	"context"
	"encoding/json"
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
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
)

func TestFinetuningStreamingStepStorageBudgetAndParentCancellation(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		_, _ = io.WriteString(w, "x")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer peer.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic-streaming", SecretAccessKey: "synthetic-streaming-secret"}, nil
	})
	ownerFile := bootstrapConfigFile(t)
	var owner ownerConfig
	if err := readConfig(ownerFile, &owner); err != nil {
		t.Fatal(err)
	}
	for _, seconds := range []int{0, 1, 600, 900, 3600, 86400} {
		owner.S3 = &storageConfig{Endpoint: peer.URL, Region: "us-east-1", CAFile: ca, StreamingTimeoutSeconds: seconds}
		data, err := json.Marshal(owner)
		if err != nil || os.WriteFile(ownerFile, data, 0600) != nil {
			t.Fatal("cannot write owner fixture")
		}
		var decoded ownerConfig
		if err := readConfig(ownerFile, &decoded); err != nil {
			t.Fatal(err)
		}
		client, err := storageClient(decoded.S3, provider)
		if err != nil {
			t.Fatalf("streaming seconds %d rejected: %v", seconds, err)
		}
		httpClient := client.Options().HTTPClient.(*http.Client)
		want := time.Duration(seconds) * time.Second
		if seconds == 0 {
			want = 120 * time.Second
		}
		wantHeader := 30 * time.Second
		if seconds > 0 {
			wantHeader = want
		}
		if httpClient.Timeout != want || decoded.TimeoutSeconds != 300 || httpClient.Transport.(*http.Transport).ResponseHeaderTimeout != wantHeader {
			t.Fatalf("streaming budget %d changed expected streaming/header or control budget: %+v", seconds, httpClient)
		}
		defer httpClient.CloseIdleConnections()
		if seconds != 900 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		started := time.Now()
		out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("synthetic-bucket"), Key: aws.String("stream")})
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
		t.Logf("CLI streaming timeout=%s, parent deadline=300ms, actual body cancellation elapsed=%s", httpClient.Timeout, time.Since(started))
	}
	for _, seconds := range []int{-1, 86401} {
		owner.S3.StreamingTimeoutSeconds = seconds
		if client, err := storageClient(owner.S3, provider); client != nil || !errors.Is(err, component.ErrConfiguration) {
			t.Fatalf("out-of-range streaming seconds %d accepted: %v", seconds, err)
		}
	}
}
