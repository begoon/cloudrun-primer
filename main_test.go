package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpstreamResponses(t *testing.T) {
	for _, tc := range []struct {
		name, route, body          string
		upstreamStatus, wantStatus int
		want                       string
	}{
		{"speed success", "speed", "test data", 200, 200, "downloaded 9 B | throughput "},
		{"speed forbidden", "speed", "denied", 403, 502, "speed download returned 403"},
		{"speed server error", "speed", "unavailable", 500, 502, "speed download returned 500"},
		{"ip success", "ip", `{"ip":"192.0.2.1"}`, 200, 200, "192.0.2.1"},
		{"ip upstream error", "ip", "not JSON", 503, 502, "IP lookup returned 503"},
		{"ip malformed JSON", "ip", "not JSON", 200, 502, "invalid character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.route == "speed" && r.UserAgent() != "curl/8.7.1" {
					t.Errorf("unexpected user agent %q", r.UserAgent())
				}
				w.WriteHeader(tc.upstreamStatus)
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			handler := ipHandler(upstream.Client(), upstream.URL)
			if tc.route == "speed" {
				handler = speedHandler(upstream.Client(), upstream.URL, time.Second)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, httptest.NewRequest("GET", "/"+tc.route, nil))
			if out.Code != tc.wantStatus || !strings.Contains(out.Body.String(), tc.want) {
				t.Fatalf("got %d %q; want %d containing %q", out.Code, out.Body.String(), tc.wantStatus, tc.want)
			}
			if tc.route == "speed" {
				if tc.wantStatus == 200 && (!strings.Contains(out.Body.String(), "url="+upstream.URL) || !strings.Contains(out.Body.String(), "/s | elapsed")) {
					t.Fatal("missing URL or throughput units")
				}
				if tc.wantStatus != 200 && strings.Contains(out.Body.String(), "downloaded") {
					t.Fatal("failed download reported as successful")
				}
			}
		})
	}
}

func TestConnectionFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	for name, handler := range map[string]http.HandlerFunc{
		"speed": speedHandler(client, "https://upstream.test", time.Second),
		"ip":    ipHandler(client, "https://upstream.test"),
	} {
		t.Run(name, func(t *testing.T) {
			out := httptest.NewRecorder()
			handler(out, httptest.NewRequest("GET", "/", nil))
			if out.Code != 502 || !strings.Contains(out.Body.String(), "connection refused") {
				t.Fatalf("got %d %q", out.Code, out.Body.String())
			}
		})
	}
}

func TestSpeedTimeoutAndCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		name := "timeout"
		if cancelCaller {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var gotErr error
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if _, ok := r.Context().Deadline(); !ok {
					t.Error("outbound request has no deadline")
				}
				if cancelCaller {
					cancel()
				}
				<-r.Context().Done()
				gotErr = r.Context().Err()
				return nil, gotErr
			})}
			out := httptest.NewRecorder()
			speedHandler(client, "https://upstream.test", 10*time.Millisecond)(out, httptest.NewRequest("GET", "/speed", nil).WithContext(ctx))
			want := context.DeadlineExceeded
			if cancelCaller {
				want = context.Canceled
			}
			if !errors.Is(gotErr, want) || out.Code != 502 {
				t.Fatalf("got %v, status %d", gotErr, out.Code)
			}
		})
	}
}

func TestSpeedInterruptedDownload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "short")
	}))
	defer upstream.Close()
	out := httptest.NewRecorder()
	speedHandler(upstream.Client(), upstream.URL, time.Second)(out, httptest.NewRequest("GET", "/speed", nil))
	body := out.Body.String()
	if out.Code != 200 || !strings.Contains(body, "error=download failed: unexpected EOF") || strings.Contains(body, "downloaded") {
		t.Fatalf("got %d %q", out.Code, body)
	}
}

type cancelBody struct {
	ctx    context.Context
	closed bool
}

func (b *cancelBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b *cancelBody) Close() error             { b.closed = true; return nil }

func TestSpeedTimeoutDuringBody(t *testing.T) {
	var body *cancelBody
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body = &cancelBody{ctx: r.Context()}
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: body, Header: make(http.Header)}, nil
	})}
	out := httptest.NewRecorder()
	speedHandler(client, "https://upstream.test", 10*time.Millisecond)(out, httptest.NewRequest("GET", "/speed", nil))
	if !body.closed || !strings.Contains(out.Body.String(), "error=download failed: context deadline exceeded") || strings.Contains(out.Body.String(), "downloaded") {
		t.Fatalf("body closed=%v, output=%q", body.closed, out.Body.String())
	}
}

type failedResponse struct {
	header    http.Header
	writes    int
	failAfter int
	flushErr  error
}

var errDisconnected = errors.New("client disconnected")

func (w *failedResponse) Header() http.Header { return w.header }
func (w *failedResponse) WriteHeader(int)     {}
func (w *failedResponse) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > w.failAfter {
		return 0, errDisconnected
	}
	return len(p), nil
}
func (w *failedResponse) FlushError() error { return w.flushErr }

type countingBody struct {
	bytes  int
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) { b.bytes += len(p); return len(p), nil }
func (b *countingBody) Close() error               { b.closed = true; return nil }

func TestSpeedStopsOnOutputFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failAfter int
		flushErr  error
		wantBytes bool
	}{
		{"initial write", 0, nil, false},
		{"initial flush", 1, errDisconnected, false},
		{"progress write", 1, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countingBody{}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Status: "200 OK", Body: body, Header: make(http.Header)}, nil
			})}
			out := &failedResponse{header: make(http.Header), failAfter: tc.failAfter, flushErr: tc.flushErr}
			speedHandler(client, "https://upstream.test", time.Second)(out, httptest.NewRequest("GET", "/speed", nil))
			if !body.closed || (body.bytes > 0) != tc.wantBytes || body.bytes > blockSize+32768 {
				t.Fatalf("read %d bytes, closed=%v", body.bytes, body.closed)
			}
			if out.writes > tc.failAfter+1 {
				t.Fatal("continued writing after disconnect")
			}
		})
	}
}

func TestProgressFlushFailure(t *testing.T) {
	out := &failedResponse{header: make(http.Header), failAfter: 1, flushErr: errDisconnected}
	writer := &Writer{writer: out, started: time.Now(), block: blockSize - 1}
	_, err := writer.Write([]byte("x"))
	if !errors.Is(err, errDisconnected) {
		t.Fatalf("got %v", err)
	}
}
