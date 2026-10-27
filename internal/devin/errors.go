package devin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"kyo-router/internal/connector"
	"kyo-router/internal/httpx"
)

// responseRecorder remembers the HTTP status and Retry-After of one RPC.
// connect-go exposes the error-path status but not the response headers, so a
// rate-limited chat call would otherwise lose its retry hint.
type responseRecorder struct {
	transport httpx.Transport

	mu         sync.Mutex
	status     int
	retryAfter string
}

func (r *responseRecorder) Do(req *http.Request) (*http.Response, error) {
	response, err := r.transport.Do(req)
	if response != nil {
		r.mu.Lock()
		r.status = response.StatusCode
		r.retryAfter = response.Header.Get("Retry-After")
		r.mu.Unlock()
	}
	return response, err
}

func (r *responseRecorder) Status() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *responseRecorder) RetryAfter() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.retryAfter
}

// httpError maps a plain HTTP status to a connector Kind. The body is never
// read for classification: only the status and the Retry-After header count.
func httpError(status int, retryAfter string, now time.Time) *connector.Error {
	kind := httpx.StatusKind(status)
	err := &connector.Error{
		Kind:    kind,
		Code:    "http_" + strconv.Itoa(status),
		Message: describeStatus(status),
		Status:  status,
	}
	if kind == connector.KindRateLimit {
		err.RetryAfter = parseRetryAfter(retryAfter, now)
	}
	return err
}

// connectError maps a Connect-RPC trailer or HTTP failure to a connector Kind.
// A non-2xx HTTP status wins over the Connect code: connect-go folds 429, 500,
// and 400 into codes that lose the table's distinction (RateLimit, Upstream,
// UpstreamBad). A 2xx with a trailer error is classified by the trailer code,
// never by the upstream message text.
func connectError(ctx context.Context, callInfo *connect.CallInfo, recorder *responseRecorder, err error, now time.Time) error {
	status := 0
	if info, ok := connecthttp.ClientInfoForContext(ctx); ok {
		status = info.ResponseStatus()
	}
	if status == 0 && recorder != nil {
		status = recorder.Status()
	}
	if status >= 400 {
		retryAfter := ""
		if recorder != nil {
			retryAfter = recorder.RetryAfter()
		}
		if retryAfter == "" && callInfo != nil {
			retryAfter = callInfo.ResponseHeader().Get("Retry-After")
		}
		return httpError(status, retryAfter, now)
	}
	var rpcErr *connect.Error
	if errors.As(err, &rpcErr) {
		// A dial, TLS, or read failure keeps its net.Error cause. Connect folds
		// it into CodeUnavailable, but the table calls it Network so the router
		// retries instead of failing over at once.
		var netErr net.Error
		if errors.As(err, &netErr) {
			return connector.NetworkError(err)
		}
		kind := connector.KindUpstreamBad
		switch rpcErr.Code() {
		case connect.CodeUnauthenticated, connect.CodePermissionDenied:
			kind = connector.KindAuth
		case connect.CodeResourceExhausted:
			kind = connector.KindRateLimit
		case connect.CodeUnavailable, connect.CodeInternal:
			kind = connector.KindUpstream
		case connect.CodeDeadlineExceeded, connect.CodeCanceled:
			kind = connector.KindNetwork
		}
		return &connector.Error{
			Kind:    kind,
			Code:    "connect_" + strings.ToLower(rpcErr.Code().String()),
			Message: rpcErr.Message(),
			Err:     err,
		}
	}
	return connector.NetworkError(err)
}

// parseRetryAfter reads both forms of Retry-After: delay seconds or HTTP date.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if delay := at.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

// describeStatus is used in error text where the status code alone is unclear.
func describeStatus(status int) string {
	if text := http.StatusText(status); text != "" {
		return fmt.Sprintf("%d %s", status, text)
	}
	return strconv.Itoa(status)
}
