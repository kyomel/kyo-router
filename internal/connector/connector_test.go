package connector

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestKindOfUnwraps(t *testing.T) {
	base := &Error{Kind: KindRateLimit, Code: "http_429", Message: "slow down", RetryAfter: 2 * time.Second}
	wrapped := fmt.Errorf("connector: %w", base)
	if got := KindOf(wrapped); got != KindRateLimit {
		t.Errorf("KindOf(wrapped) = %q, want RateLimit", got)
	}
	if got := KindOf(errors.New("plain")); got != "" {
		t.Errorf("KindOf(plain) = %q, want empty", got)
	}
	var target *Error
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As did not find the connector error")
	}
	if target.RetryAfter != 2*time.Second {
		t.Errorf("retry after = %v", target.RetryAfter)
	}
}

func TestErrorText(t *testing.T) {
	err := &Error{Kind: KindAuth, Code: "http_401", Message: "Unauthorized"}
	if got := err.Error(); got != "Auth http_401: Unauthorized" {
		t.Errorf("Error() = %q", got)
	}
}
