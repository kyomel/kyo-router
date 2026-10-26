package devin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

var fakeNow = time.Unix(1_700_000_000, 0)

type staticTokenSource struct {
	auth Auth
	err  error
}

func (s staticTokenSource) Token(context.Context) (Auth, error) {
	return s.auth, s.err
}

func (s staticTokenSource) Invalidate() {}

func newTestConnector(t *testing.T, server *httptest.Server, auth Auth, key string) *Connector {
	t.Helper()
	dev, err := New(Options{
		Credentials: Credentials{
			APIKey:  NormalizeSessionToken(key),
			BaseURL: server.URL,
			Source:  SourceEnv,
			Kind:    KeyKindModelAPI,
		},
		TokenSource: staticTokenSource{auth: auth},
		Transport:   server.Client(),
		Clock:       &fakeClock{now: fakeNow},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return dev
}

const (
	connectFlagCompressed = 0x01
	connectFlagEndStream  = 0x02
)

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buffer.Bytes()
}

func protoBytes(t *testing.T, message proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal proto: %v", err)
	}
	return payload
}

func writeConnectFrame(t *testing.T, w io.Writer, flag byte, payload []byte) {
	t.Helper()
	var header [5]byte
	header[0] = flag
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		t.Fatalf("write frame header: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write frame payload: %v", err)
	}
}

func writeConnectMessage(t *testing.T, w io.Writer, message proto.Message, compressed bool) {
	t.Helper()
	payload := protoBytes(t, message)
	if compressed {
		writeConnectFrame(t, w, connectFlagCompressed, gzipBytes(t, payload))
		return
	}
	writeConnectFrame(t, w, 0, payload)
}

func writeEndStream(t *testing.T, w io.Writer, trailer string) {
	t.Helper()
	writeConnectFrame(t, w, connectFlagEndStream, []byte(trailer))
}

func readConnectRequest(t *testing.T, r *http.Request, message proto.Message) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if len(body) < 5 {
		t.Fatalf("request body = %d bytes, want a Connect frame", len(body))
	}
	flag := body[0]
	if flag&connectFlagCompressed == 0 {
		t.Fatalf("request frame flag = %#x, want gzip bit", flag)
	}
	size := binary.BigEndian.Uint32(body[1:5])
	if int(size) != len(body)-5 {
		t.Fatalf("frame size = %d, payload = %d", size, len(body)-5)
	}
	payload, err := gunzip(body[5:])
	if err != nil {
		t.Fatalf("gunzip request: %v", err)
	}
	if err := proto.Unmarshal(payload, message); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}
