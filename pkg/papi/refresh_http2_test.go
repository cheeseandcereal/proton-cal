package papi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestRefreshDoesNotReplayAfterHTTP2StreamError(t *testing.T) {
	var refreshes atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("expected HTTP/2")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.EnableHTTP2 = true
	srv.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
		"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			preface := make([]byte, len(http2.ClientPreface))
			if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != http2.ClientPreface {
				t.Errorf("HTTP/2 preface: %q, %v", preface, err)
				return
			}
			fr := http2.NewFramer(conn, conn)
			if err := fr.WriteSettings(); err != nil {
				t.Error(err)
				return
			}
			var body bytes.Buffer
			for {
				frame, err := fr.ReadFrame()
				if err != nil {
					return
				}
				switch f := frame.(type) {
				case *http2.SettingsFrame:
					if !f.IsAck() {
						_ = fr.WriteSettingsAck()
					}
				case *http2.DataFrame:
					body.Write(f.Data())
					if !f.StreamEnded() {
						continue
					}
					var req proton.AuthRefreshReq
					if err := json.Unmarshal(body.Bytes(), &req); err != nil || req.RefreshToken != "r0" {
						t.Errorf("refresh body = %s, %v", body.Bytes(), err)
					}
					body.Reset()
					if refreshes.Add(1) == 1 {
						// Reset only after consuming the complete refresh token.
						_ = fr.WriteRSTStream(f.StreamID, http2.ErrCodeProtocol)
						continue
					}
					var headers bytes.Buffer
					encoder := hpack.NewEncoder(&headers)
					_ = encoder.WriteField(hpack.HeaderField{Name: ":status", Value: "422"})
					_ = fr.WriteHeaders(http2.HeadersFrameParam{
						StreamID: f.StreamID, EndHeaders: true, EndStream: true, BlockFragment: headers.Bytes(),
					})
				}
			}
		},
	}
	srv.StartTLS()
	defer srv.Close()
	client, _ := testClient(t, nil)
	client.baseURL, client.httpc = srv.URL, srv.Client()
	client.httpc.Timeout = 5 * time.Second
	defer client.httpc.CloseIdleConnections()
	_, err := client.rotateTokens(t.Context(), client.sess)
	if err == nil || refreshes.Load() != 1 {
		t.Fatalf("refresh error = %v, submitted bodies = %d", err, refreshes.Load())
	}
}
