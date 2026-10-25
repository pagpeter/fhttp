//go:build !nethttpomithttp2

package http

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/bogdanfinn/fhttp/http2/hpack"
)

// bundledServerConn is a raw client connection to the HTTP/2 server bundled
// in h2_bundle.go, for tests that need frames a real client would not send.
type bundledServerConn struct {
	t       *testing.T
	fr      *http2Framer
	hbuf    bytes.Buffer
	encoder *hpack.Encoder
}

func newBundledServerConn(t *testing.T, handler HandlerFunc) *bundledServerConn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		new(http2Server).ServeConn(c, &http2ServeConnOpts{Handler: handler})
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Close()
		ln.Close()
	})
	c.SetDeadline(time.Now().Add(10 * time.Second))

	bc := &bundledServerConn{t: t, fr: http2NewFramer(c, c)}
	bc.encoder = hpack.NewEncoder(&bc.hbuf)
	if _, err := c.Write(http2clientPreface); err != nil {
		t.Fatal(err)
	}
	if err := bc.fr.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := bc.fr.ReadFrame()
		if err != nil {
			t.Fatalf("reading server SETTINGS: %v", err)
		}
		if sf, ok := f.(*http2SettingsFrame); ok && !sf.IsAck() {
			break
		}
	}
	if err := bc.fr.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}
	return bc
}

func (bc *bundledServerConn) writeRequestHeaders(streamID uint32, endStream bool) {
	bc.t.Helper()
	bc.hbuf.Reset()
	for _, kv := range [][2]string{
		{":method", "GET"},
		{":scheme", "https"},
		{":authority", "example.com"},
		{":path", "/"},
	} {
		bc.encoder.WriteField(hpack.HeaderField{Name: kv[0], Value: kv[1]})
	}
	if err := bc.fr.WriteHeaders(http2HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: bc.hbuf.Bytes(),
		EndStream:     endStream,
		EndHeaders:    true,
	}); err != nil {
		bc.t.Fatal(err)
	}
}

// A stream error must cancel the handler's request context
// (golang.org/issue/67036). This mirrors TestServerRequestCancelOnError
// in package http2.
func TestBundledServerRequestCancelOnError(t *testing.T) {
	recvc := make(chan struct{}) // handler has started
	donec := make(chan struct{}) // handler has finished
	bc := newBundledServerConn(t, func(w ResponseWriter, r *Request) {
		close(recvc)
		<-r.Context().Done()
		close(donec)
	})

	bc.writeRequestHeaders(1, true)
	select {
	case <-recvc:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}

	// A second set of request headers on the stream is a stream error.
	bc.writeRequestHeaders(1, true)
	select {
	case <-donec:
	case <-time.After(5 * time.Second):
		t.Fatal("handler context was not canceled after the stream was reset")
	}
}
