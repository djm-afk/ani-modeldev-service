package service

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
)

// metricSchema names the only line format the trainer writes today.
const metricSchema = "ani.metric.v1"

// metricsLine is one flattened ani.metric.v1 record. Unknown fields are ignored
// because rank and timestamp are not part of the published metric shape.
type metricsLine struct {
	Schema string   `json:"schema"`
	Name   string   `json:"name"`
	Value  *float64 `json:"value"`
	Step   *int64   `json:"step"`
}

// parseMetrics decodes an ani.metric.v1 JSONL document into published metrics.
// Malformed lines, foreign schemas and non-finite values are skipped: one bad
// record must not hide a usable curve. A completely unusable document yields no
// metrics rather than an error.
func parseMetrics(document []byte) []*trainingv1.Metric {
	var metrics []*trainingv1.Metric
	for _, line := range bytes.Split(document, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record metricsLine
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if record.Schema != metricSchema || record.Name == "" || record.Value == nil {
			continue
		}
		value := *record.Value
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		metric := &trainingv1.Metric{Name: record.Name, DecimalValue: strconv.FormatFloat(value, 'g', -1, 64)}
		if record.Step != nil {
			step := *record.Step
			metric.Step = &step
		}
		metrics = append(metrics, metric)
	}
	return metrics
}
