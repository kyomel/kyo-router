package deepseek

import (
	"io"
	"net/http"

	"kyo-router/internal/httpx"
	"kyo-router/internal/oai"
)

// upstreamHTTPError maps a non-2xx status to a Kind. The body is read for the
// message only: classification never looks at text.
func upstreamHTTPError(resp *http.Response) error {
	var message string
	if body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10)); err == nil {
		if detail := oai.ParseError(body); detail.Message != "" {
			message = detail.Message
		}
	}
	return httpx.UpstreamError(resp, httpx.StatusKind(resp.StatusCode), message)
}
