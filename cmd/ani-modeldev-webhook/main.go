package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdout); err != nil {
		// No configuration, private-key path, token or Kubernetes error text.
		fmt.Fprintln(os.Stderr, "modeldev webhook startup or serving failed")
		os.Exit(1)
	}
}

func execute(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("ani-modeldev-webhook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "operator-owned JSON configuration")
	check := flags.Bool("check-config", false, "validate frozen profiles without Kubernetes or TLS startup")
	certificate := flags.String("tls-cert", "", "projected server certificate")
	key := flags.String("tls-key", "", "projected server private key")
	if flags.Parse(arguments) != nil || *path == "" || flags.NArg() != 0 {
		return modeldevwebhook.ErrConfiguration
	}
	config, err := modeldevwebhook.LoadConfig(*path)
	if err != nil {
		return err
	}
	if *check {
		return json.NewEncoder(output).Encode(map[string]any{"valid": true, "profiles": len(config.Profiles), "namespace_bindings": len(config.Bindings), "registered": false})
	}
	if *certificate != "" {
		config.CertificateFile = *certificate
	}
	if *key != "" {
		config.PrivateKeyFile = *key
	}
	timeout := config.DependencyTimeoutMilliseconds
	if timeout == 0 {
		timeout = 2000
	}
	reader, err := modeldevwebhook.NewInClusterReader(time.Duration(timeout) * time.Millisecond)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(output, nil))
	handler, err := modeldevwebhook.New(config, reader, logger)
	if err != nil {
		return err
	}
	server, err := modeldevwebhook.NewServer(config, handler)
	if err != nil {
		return err
	}
	// net/http's default TLS error log can include client endpoints. Keep the
	// process log limited to stable aggregate admission decisions instead.
	server.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)
	shutdownFinished := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		close(shutdownFinished)
	}()
	err = server.ListenAndServeTLS("", "")
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownFinished
		return nil
	}
	return err
}
