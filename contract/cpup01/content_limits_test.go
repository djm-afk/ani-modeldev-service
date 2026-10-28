package cpup01_test

import (
	"errors"
	"math"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

func TestContentLimitUsesExplicitPolicyAndSeparatesTarOverhead(t *testing.T) {
	policy := cpup01.ContentLimits{MaxContentBytes: 17179869184}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := policy.Check(cpup01.ContentDataset, 17179869184); err != nil {
		t.Fatalf("content at the configured limit: %v", err)
	}
	var exceeded *cpup01.LimitViolation
	if err := policy.Check(cpup01.ContentDataset, 17179869185); !errors.As(err, &exceeded) || exceeded.Reason != "CONTENT_LIMIT_EXCEEDED" || exceeded.Kind != "dataset" || exceeded.Actual != 17179869185 || exceeded.Limit != 17179869184 {
		t.Fatalf("structured over-limit result: %#v, %v", exceeded, err)
	}
	bound, err := cpup01.TarReadBound(policy.MaxContentBytes)
	if err != nil || bound != 17199267840 {
		t.Fatalf("tar includes bounded container overhead: %d, %v", bound, err)
	}
	for _, value := range []int64{0, -1, math.MaxInt64} {
		if (cpup01.ContentLimits{MaxContentBytes: value}).Validate() == nil {
			t.Fatalf("unconfigured/unrepresentable policy accepted: %d", value)
		}
	}
	lower := cpup01.ContentLimits{MaxContentBytes: 1024}
	if lower.Check(cpup01.ContentOutput, 1025) == nil || policy.Check(cpup01.ContentOutput, 1025) != nil {
		t.Fatal("two explicit configurations must apply without a hidden content constant")
	}
}
