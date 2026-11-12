package main

import (
	"errors"
	"os"
	"strings"
)

// datasetWorkerOwner is the lease owner identity of this process. It must be
// unique per replica: the lease SQL treats a re-acquisition by the same owner as
// a renewal that keeps the epoch, so two replicas sharing one owner string could
// both hold the same lease and dispatch the same task twice.
//
// The pod name is the natural identity. A hostname is the fallback for a
// non-Kubernetes run. When neither is available the caller refuses to start
// rather than silently sharing an owner with another replica.
func datasetWorkerOwner() (string, error) {
	const prefix = "modeldev:service:"
	if pod := strings.TrimSpace(os.Getenv("POD_NAME")); pod != "" {
		return prefix + pod, nil
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("dataset worker owner unavailable: set POD_NAME or provide a hostname")
	}
	return prefix + strings.TrimSpace(host), nil
}
