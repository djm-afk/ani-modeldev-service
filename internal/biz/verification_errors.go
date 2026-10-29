package biz

import "errors"

// Complete-source outages and invalid source content are independent classes.
// No storage response text, endpoint URL or credentials enter either error.
var (
	ErrObjectSourceUnavailable = errors.New("OBJECT_SOURCE_UNAVAILABLE")
	ErrInputSourceUnavailable  = errors.New("INPUT_SOURCE_UNAVAILABLE")
)
