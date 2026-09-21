package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ct "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

type fakeLogDocker struct {
	tty     bool
	payload []byte
	options ct.LogsOptions
	reader  io.ReadCloser
	logsErr error
}

func (f *fakeLogDocker) ContainerInspect(ctx context.Context, name string) (ct.InspectResponse, error) {
	return ct.InspectResponse{ContainerJSONBase: &ct.ContainerJSONBase{ID: "stable-id"}, Config: &ct.Config{Tty: f.tty}}, nil
}
func (f *fakeLogDocker) ContainerLogs(ctx context.Context, id string, o ct.LogsOptions) (io.ReadCloser, error) {
	f.options = o
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	if f.reader != nil {
		return f.reader, nil
	}
	return io.NopCloser(bytes.NewReader(f.payload)), nil
}
func logFixture(f *fakeLogDocker) *logService {
	return &logService{docker: f, authorize: func(*http.Request) bool { return true }, known: func(s string) bool { return s == "worker" }, slots: make(chan struct{}, 1)}
}

func TestLogHistoryDemultiplexesAndRedacts(t *testing.T) {
	var buf bytes.Buffer
	stamp := "2026-09-21T00:00:00.123456789Z "
	stdcopy.NewStdWriter(&buf, stdcopy.Stdout).Write([]byte(stamp + "hello <script>alert(1)</script>\n"))
	stdcopy.NewStdWriter(&buf, stdcopy.Stderr).Write([]byte(stamp + "password=abc Bearer sirpat_example_secret\n"))
	f := &fakeLogDocker{payload: buf.Bytes()}
	w := httptest.NewRecorder()
	logFixture(f).serve(w, httptest.NewRequest("GET", "/api/logs?container=worker&tail=100", nil))
	s := w.Body.String()
	if w.Code != 200 || !strings.Contains(s, `"stream":"stdout"`) || !strings.Contains(s, `"stream":"stderr"`) || !strings.Contains(s, "event: end") {
		t.Fatal("missing separated log output")
	}
	if strings.Contains(s, "abc") || strings.Contains(s, "sirpat_example_secret") || !strings.Contains(s, "[REDACTED]") {
		t.Fatal("credential not redacted")
	}
	if f.options.Follow || !f.options.ShowStderr || !f.options.Timestamps || f.options.Tail != "100" {
		t.Fatal("incorrect Docker log options")
	}
	if w.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatal("nginx must not buffer live logs")
	}
}
func TestTTYLogsAndPartialFinalLine(t *testing.T) {
	f := &fakeLogDocker{tty: true, payload: []byte("2026-09-21T00:00:00Z terminal output")}
	w := httptest.NewRecorder()
	logFixture(f).serve(w, httptest.NewRequest("GET", "/api/logs?container=worker", nil))
	if !strings.Contains(w.Body.String(), `"stream":"tty"`) || !strings.Contains(w.Body.String(), "terminal output") {
		t.Fatal("TTY output lost")
	}
}
func TestLogsRejectAccessAndInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		method, url string
		allowed     bool
		code        int
	}{{"GET", "/api/logs?container=worker", false, 403}, {"POST", "/api/logs?container=worker", true, 405}, {"GET", "/api/logs?container=other", true, 404}, {"GET", "/api/logs?container=worker&tail=1001", true, 400}, {"GET", "/api/logs?container=worker&period=999h", true, 400}} {
		s := logFixture(&fakeLogDocker{})
		s.authorize = func(*http.Request) bool { return tc.allowed }
		w := httptest.NewRecorder()
		s.serve(w, httptest.NewRequest(tc.method, tc.url, nil))
		if w.Code != tc.code {
			t.Errorf("%+v returned %d", tc, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/logs", nil)
	r.Header.Set("X-Auth-Role", "admin")
	r.Header.Set("X-Auth-User-Id", "spoofed")
	r.RemoteAddr = "not-a-peer"
	if authorizeLogs(r) {
		t.Fatal("headers alone cannot grant access")
	}
}
func TestLogResumeUsesTimestampAndBounds(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/logs?follow=1&period=15m", nil)
	stamp := "2026-09-21T00:00:00.123456789Z"
	r.Header.Set("Last-Event-ID", stamp)
	o, e := logOptions(r)
	if e != nil || o.Since != stamp || !o.Follow {
		t.Fatal("resume options incorrect")
	}
	r.Header.Set("Last-Event-ID", "junk")
	if _, e = logOptions(r); e == nil {
		t.Fatal("invalid resume accepted")
	}
}
func TestSplitCredentialsAndOversizedLines(t *testing.T) {
	ch := make(chan logRecord, 4)
	w := &logLineWriter{ctx: context.Background(), stream: "stdout", output: ch}
	w.Write([]byte("api_key=very"))
	w.Write([]byte("secret\n"))
	if got := (<-ch).Text; strings.Contains(got, "verysecret") || !strings.Contains(got, "[REDACTED]") {
		t.Fatal("split secret leaked")
	}
	w.Write([]byte(strings.Repeat("x", 20000) + "\n"))
	if got := (<-ch).Text; got != "[oversized log line omitted]" {
		t.Fatal("unbounded line")
	}
}
func TestUnsupportedLogDriver(t *testing.T) {
	s := logFixture(&fakeLogDocker{logsErr: errdefs.NotImplemented(io.EOF)})
	w := httptest.NewRecorder()
	s.serve(w, httptest.NewRequest("GET", "/api/logs?container=worker", nil))
	if w.Code != 501 {
		t.Fatal("expected explicit unsupported-driver error")
	}
}
func TestLogViewerLimit(t *testing.T) {
	s := logFixture(&fakeLogDocker{})
	s.slots <- struct{}{}
	w := httptest.NewRecorder()
	s.serve(w, httptest.NewRequest("GET", "/api/logs?container=worker", nil))
	if w.Code != 429 {
		t.Fatal("viewer limit not enforced")
	}
}

type closeSignal struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (r *closeSignal) Close() error {
	r.once.Do(func() { close(r.closed) })
	return r.ReadCloser.Close()
}
func TestLogStreamClosesOnClientDisconnect(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	observed := &closeSignal{ReadCloser: reader, closed: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(logFixture(&fakeLogDocker{reader: observed}).serve))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?container=worker&follow=1", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	cancel()
	select {
	case <-observed.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Docker stream leaked after disconnect")
	}
}
