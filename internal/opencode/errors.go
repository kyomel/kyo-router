package opencode

import (
	"io"
	"net/http"

	"kyo-router/internal/connector"
	"kyo-router/internal/httpx"
	"kyo-router/internal/oai"
)

// upstreamHTTPError maps a non-2xx status plus the provider error envelope to
// a Kind (02-connectors.md error table). Typed code/type fields classify
// first; the two message-only signals match exactly - never substring.
func upstreamHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	detail := oai.ParseError(body)

	connErr := httpx.UpstreamError(resp, kindFromProviderSignal(detail, kindFromStatus(resp.StatusCode)), detail.Message)
	if detail.Code != "" {
		connErr.Code = detail.Code
	}
	return connErr
}

func kindFromStatus(status int) connector.Kind {
	if status == http.StatusBadRequest {
		return connector.KindBadRequest
	}
	return httpx.StatusKind(status)
}

// kindFromProviderSignal applies the doc's provider-signal rows. Typed
// code/type fields outrank the generic status: FreeTierError can arrive on
// a 403 yet means Unsupported, not Auth. "Model is unavailable" and
// "Insufficient account funds" arrive as bare messages and match exactly.
func kindFromProviderSignal(detail oai.ErrorDetail, fallback connector.Kind) connector.Kind {
	signal := detail.Code
	if signal == "" {
		signal = detail.Type
	}
	switch signal {
	case "MissingSessionID":
		return connector.KindBadRequest
	case "ModelProtocolUnsupported", "FreeTierError":
		return connector.KindUnsupported
	}
	switch detail.Message {
	case "Model is unavailable":
		return connector.KindUnsupported
	case "Insufficient account funds":
		return connector.KindAuth
	}
	return fallback
}
