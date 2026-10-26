// Package httpx holds the shared HTTP pieces every OpenAI-compatible
// connector uses. The SSE scanner is its first member; backoff and the shared
// client land with the OpenCode connector (docs/04-milestones.md, M1).
package httpx

import (
	"bufio"
	"io"
	"strings"
)

// Done is the SSE terminator upstreams send instead of closing cleanly.
const Done = "[DONE]"

// SSEScanner yields the payload of each data: line in order. Comments,
// event:, id:, and retry: lines are skipped; blank lines delimit events.
type SSEScanner struct {
	scanner *bufio.Scanner
}

// NewSSEScanner wraps an SSE response body. The caller still owns closing it.
func NewSSEScanner(body io.Reader) *SSEScanner {
	scanner := bufio.NewScanner(body)
	// SSE data lines carry whole JSON objects; 64KB of headroom covers
	// oversized chunks without unbounded growth.
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	return &SSEScanner{scanner: scanner}
}

// Next returns the next data payload. ok is false at EOF or on a read error;
// call Err to tell them apart.
func (s *SSEScanner) Next() (data string, ok bool) {
	for s.scanner.Scan() {
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
	}
	return "", false
}

// Err reports the read failure that stopped Next, if any.
func (s *SSEScanner) Err() error { return s.scanner.Err() }
