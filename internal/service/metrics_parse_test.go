package service

import (
	"strings"
	"testing"
)

func TestParseMetricsKeepsValidLinesInOrder(t *testing.T) {
	document := strings.Join([]string{
		`{"schema":"ani.metric.v1","step":1,"name":"train.loss","value":1.5,"rank":0,"timestamp":"2026-10-22T00:00:01+00:00"}`,
		`{"schema":"ani.metric.v1","step":2,"name":"train.loss","value":0.875,"rank":0,"timestamp":"2026-10-22T00:00:02+00:00"}`,
	}, "\n")
	metrics := parseMetrics([]byte(document))
	if len(metrics) != 2 {
		t.Fatalf("metrics=%d, want 2", len(metrics))
	}
	if metrics[0].Name != "train.loss" || metrics[0].DecimalValue != "1.5" || metrics[0].Step == nil || *metrics[0].Step != 1 {
		t.Fatalf("first metric=%v", metrics[0])
	}
	if metrics[1].DecimalValue != "0.875" || *metrics[1].Step != 2 {
		t.Fatalf("second metric=%v", metrics[1])
	}
}

func TestParseMetricsSkipsUnusableLines(t *testing.T) {
	document := strings.Join([]string{
		`not json`,
		`{"schema":"other.schema.v1","step":1,"name":"train.loss","value":1.0}`,
		`{"schema":"ani.metric.v1","step":2,"name":"","value":1.0}`,
		`{"schema":"ani.metric.v1","step":3,"name":"train.loss"}`,
		`{"schema":"ani.metric.v1","step":4,"name":"train.loss","value":"not-a-number"}`,
		`{"schema":"ani.metric.v1","step":5,"name":"train.loss","value":0.5}`,
		`   `,
	}, "\n")
	metrics := parseMetrics([]byte(document))
	if len(metrics) != 1 {
		t.Fatalf("metrics=%d, want 1", len(metrics))
	}
	if *metrics[0].Step != 5 || metrics[0].DecimalValue != "0.5" {
		t.Fatalf("metric=%v", metrics[0])
	}
}

func TestParseMetricsRejectsNonFiniteValues(t *testing.T) {
	// JSON has no literal NaN/Inf, so an overflowing exponent is how a trainer
	// can still emit an unusable value.
	document := strings.Join([]string{
		`{"schema":"ani.metric.v1","step":1,"name":"train.loss","value":1e400}`,
		`{"schema":"ani.metric.v1","step":2,"name":"train.loss","value":-1e400}`,
		`{"schema":"ani.metric.v1","step":3,"name":"train.loss","value":0.25}`,
	}, "\n")
	metrics := parseMetrics([]byte(document))
	if len(metrics) != 1 || *metrics[0].Step != 3 {
		t.Fatalf("metrics=%v, want only step 3", metrics)
	}
}

func TestParseMetricsEmptyDocument(t *testing.T) {
	for _, document := range []string{"", "\n", "  \n\t\n"} {
		if metrics := parseMetrics([]byte(document)); len(metrics) != 0 {
			t.Fatalf("document %q produced %d metrics", document, len(metrics))
		}
	}
}

func TestParseMetricsOmitsAbsentStep(t *testing.T) {
	metrics := parseMetrics([]byte(`{"schema":"ani.metric.v1","name":"train.loss","value":0.5}`))
	if len(metrics) != 1 || metrics[0].Step != nil {
		t.Fatalf("metrics=%v, want one metric without step", metrics)
	}
}
