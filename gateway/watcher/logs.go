package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	ct "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

type logDocker interface {
	ContainerInspect(context.Context, string) (ct.InspectResponse, error)
	ContainerLogs(context.Context, string, ct.LogsOptions) (io.ReadCloser, error)
}
type logService struct {
	docker    logDocker
	authorize func(*http.Request) bool
	known     func(string) bool
	slots     chan struct{}
}

var liveLogs *logService

// Only the real gateway may assert an administrator identity. Client headers
// alone are insufficient, including from another container on the bridge.
func authorizeLogs(r *http.Request) bool {
	if r.Header.Get("X-Auth-Role") != "admin" || r.Header.Get("X-Auth-User-Id") == "" {
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, nginxContainer)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ip.IP.Equal(net.ParseIP(peer)) {
			return true
		}
	}
	return false
}
func knownLogContainer(name string) bool {
	liveMonitor.mu.RLock()
	defer liveMonitor.mu.RUnlock()
	for _, n := range liveMonitor.snapshot.Nodes {
		if n.Name == name && n.State != "missing" {
			return true
		}
	}
	return false
}

type logRecord struct {
	Time   string `json:"time"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

var secretPatterns = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`sirpat_[A-Za-z0-9_-]+`), `[REDACTED]`},
	{regexp.MustCompile(`(?i)(Bearer\s+)[^\s"']+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)((?:password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|token|authorization|cookie)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`), `${1}[REDACTED]`},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^/@\s]+@`), `${1}[REDACTED]@`},
	{regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07]*(?:\x07|\x1b\\\\)"), ``},
}

func cleanLog(text string) string {
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllString(text, p.replacement)
	}
	return strings.Map(func(r rune) rune {
		if (r < 32 && r != '\t') || r == 127 {
			return -1
		}
		return r
	}, text)
}

// Docker writes can split both timestamps and credentials. Buffer whole lines
// before redaction, with a hard bound even for a process that never prints LF.
type logLineWriter struct {
	ctx       context.Context
	stream    string
	buffer    []byte
	oversized bool
	output    chan<- logRecord
}

func (w *logLineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			if err := w.flush(); err != nil {
				return 0, err
			}
			continue
		}
		if w.oversized {
			continue
		}
		if len(w.buffer) >= 16384 {
			w.buffer = nil
			w.oversized = true
			continue
		}
		w.buffer = append(w.buffer, b)
	}
	return len(p), nil
}
func (w *logLineWriter) flush() error {
	text := string(w.buffer)
	stamp := ""
	if w.oversized {
		text = "[oversized log line omitted]"
	} else if first, rest, ok := strings.Cut(text, " "); ok {
		if _, err := time.Parse(time.RFC3339Nano, first); err == nil {
			stamp = first
			text = rest
		}
	}
	record := logRecord{stamp, w.stream, cleanLog(text)}
	w.buffer = nil
	w.oversized = false
	select {
	case w.output <- record:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

func logOptions(r *http.Request) (ct.LogsOptions, error) {
	tail := 200
	if s := r.URL.Query().Get("tail"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 1000 {
			return ct.LogsOptions{}, fmt.Errorf("tail must be 1–1000")
		}
		tail = n
	}
	since := ""
	if period := r.URL.Query().Get("period"); period != "" && period != "all" {
		dur, e := time.ParseDuration(period)
		if e != nil || (dur != 15*time.Minute && dur != time.Hour && dur != 24*time.Hour) {
			return ct.LogsOptions{}, fmt.Errorf("unsupported period")
		}
		since = time.Now().Add(-dur).UTC().Format(time.RFC3339Nano)
	}
	if resume := r.Header.Get("Last-Event-ID"); resume != "" {
		if _, e := time.Parse(time.RFC3339Nano, resume); e != nil {
			return ct.LogsOptions{}, fmt.Errorf("invalid resume timestamp")
		}
		since = resume
	}
	return ct.LogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Follow: r.URL.Query().Get("follow") == "1", Tail: strconv.Itoa(tail), Since: since}, nil
}

func (s *logService) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	if !s.authorize(r) {
		http.Error(w, "Administrator login through the gateway is required", 403)
		return
	}
	name := r.URL.Query().Get("container")
	if name == "" || !s.known(name) {
		http.Error(w, "Container not available in the monitored inventory", 404)
		return
	}
	options, err := logOptions(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "Too many log viewers; retry shortly", 429)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	info, err := s.docker.ContainerInspect(ctx, name)
	if err != nil {
		http.Error(w, "Container is no longer available", 404)
		return
	}
	stream, err := s.docker.ContainerLogs(ctx, info.ID, options)
	if err != nil {
		code := 502
		if errdefs.IsNotImplemented(err) {
			code = 501
		}
		http.Error(w, "Docker logs unavailable; this logging driver may not support reading logs", code)
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	emit := func(event string, value any, id string) error {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		body, _ := json.Marshal(value)
		if id != "" {
			if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
			return err
		}
		return controller.Flush()
	}
	fmt.Fprint(w, "retry: 2000\n\n")
	if emit("meta", map[string]any{"container": name, "tail": options.Tail, "follow": options.Follow}, "") != nil {
		return
	}
	lines := make(chan logRecord, 64)
	done := make(chan error, 1)
	go func() {
		out := &logLineWriter{ctx: ctx, stream: "stdout", output: lines}
		errs := &logLineWriter{ctx: ctx, stream: "stderr", output: lines}
		var e error
		if info.Config != nil && info.Config.Tty {
			out.stream = "tty"
			_, e = io.Copy(out, stream)
		} else {
			_, e = stdcopy.StdCopy(out, errs, stream)
		}
		if len(out.buffer) > 0 || out.oversized {
			_ = out.flush()
		}
		if len(errs.buffer) > 0 || errs.oversized {
			_ = errs.flush()
		}
		done <- e
		close(lines)
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				if ctx.Err() != nil {
					return
				}
				e := <-done
				if e != nil && ctx.Err() == nil {
					_ = emit("problem", map[string]string{"message": "Docker log stream interrupted; reconnect or reload"}, "")
				} else {
					_ = emit("end", map[string]string{"message": "End of available logs"}, "")
				}
				return
			}
			if emit("log", line, line.Time) != nil {
				return
			}
		case <-heartbeat.C:
			if emit("heartbeat", map[string]string{"time": time.Now().UTC().Format(time.RFC3339)}, "") != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
