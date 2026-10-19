package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/client"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var generationMu sync.Mutex
var managedHTTP = &http.Client{Timeout: 5 * time.Second}

type managedRoute struct {
	Host     string `json:"host"`
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}
type domainClaim struct {
	Host    string `json:"host"`
	Kind    string `json:"kind"`
	Owner   string `json:"owner"`
	Project string `json:"project"`
	Service string `json:"service"`
}
type managedSnapshot struct {
	Revision string         `json:"revision"`
	Routes   []managedRoute `json:"routes"`
	Claims   []domainClaim  `json:"claims"`
	Token    string         `json:"-"`
}

func domainsToken() string {
	b, _ := os.ReadFile(getenv("DOMAINS_TOKEN_FILE", "/run/sir-domains/internal-token"))
	return strings.TrimSpace(string(b))
}
func fetchManaged(ctx context.Context) (*managedSnapshot, error) {
	token := domainsToken()
	if token == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", getenv("DOMAINS_URL", "http://sir-domains-api:8000")+"/internal/snapshot", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Domains-Token", token)
	r, err := managedHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("registry returned %d", r.StatusCode)
	}
	s := &managedSnapshot{Token: token}
	err = json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(s)
	return s, err
}
func nativeClaimAllowed(s *managedSnapshot, host string, labels map[string]string) bool {
	if s == nil {
		return true
	}
	for _, c := range s.Claims {
		if c.Host == host {
			if c.Kind == "user" {
				return false
			}
			if host == internalHost && c.Kind == "system" {
				return true
			}
			return c.Project != "" && c.Service != "" && c.Project == labels["com.docker.compose.project"] && c.Service == labels["com.docker.compose.service"]
		}
	}
	return false
}

var managedHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.[a-z0-9.-]+$`)
var managedIDPattern = regexp.MustCompile(`^[a-f0-9-]{36}$`)
var managedTokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func appendManaged(s *managedSnapshot, contents map[string]string, routes map[string]route, used map[string]string) error {
	if s == nil {
		return nil
	}
	if !managedTokenPattern.MatchString(s.Token) {
		return fmt.Errorf("invalid internal token format")
	}
	for _, r := range s.Routes {
		if !managedHostPattern.MatchString(r.Host) || !strings.HasSuffix(r.Host, "."+domain) || !managedIDPattern.MatchString(r.ID) {
			return fmt.Errorf("invalid managed route")
		}
		if _, ok := used[r.Host]; ok {
			return fmt.Errorf("domain conflict: %s", r.Host)
		}
		name := "managed-" + r.ID
		contents[name] = fmt.Sprintf(`server {
 listen 80;
 server_name %s;
 client_max_body_size 100m;
 location / {
  proxy_pass http://host.docker.internal:18081;
  proxy_http_version 1.1;
  proxy_set_header Host $host;
  proxy_set_header X-Domains-Token "%s";
  proxy_set_header X-Auth-User-Id "";
  proxy_set_header X-Auth-Email "";
  proxy_set_header X-Auth-Role "";
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection "upgrade";
  proxy_buffering off;
  proxy_read_timeout 3600s;
 }
}
`, r.Host, s.Token)
		routes[name] = route{Name: name, Hostname: r.Host, URL: "https://" + r.Host, IP: "host.docker.internal", Port: "18081", Auth: true, RegisteredAt: time.Now().Format("15:04:05")}
	}
	return nil
}
func reportManaged(s *managedSnapshot, applyErr error) {
	if s == nil {
		return
	}
	detail := ""
	if applyErr != nil {
		detail = applyErr.Error()
	}
	b, _ := json.Marshal(map[string]string{"revision": s.Revision, "error": detail})
	req, _ := http.NewRequest("POST", getenv("DOMAINS_URL", "http://sir-domains-api:8000")+"/internal/applied", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domains-Token", s.Token)
	if r, e := managedHTTP.Do(req); e == nil {
		r.Body.Close()
	}
}
func managedPoll(ctx context.Context, cli *client.Client) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if domainsToken() != "" {
				generateConfigs(ctx, cli)
			}
		}
	}
}
func backupConfigs() map[string][]byte {
	out := map[string][]byte{}
	files, _ := filepath.Glob(filepath.Join(confDir, "*.conf"))
	for _, f := range files {
		if b, e := os.ReadFile(f); e == nil {
			out[f] = b
		}
	}
	return out
}
func restoreConfigs(old map[string][]byte) {
	files, _ := filepath.Glob(filepath.Join(confDir, "*.conf"))
	for _, f := range files {
		os.Remove(f)
	}
	for f, b := range old {
		os.WriteFile(f, b, 0644)
	}
}

func sameConfigs(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
