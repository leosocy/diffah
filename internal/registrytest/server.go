// Package registrytest spins an in-process OCI distribution registry
// for diffah's integration tests. Wraps go-containerregistry's in-process
// registry with optional Basic-auth, bearer-token, TLS, fault-injection,
// and access-logging middleware. Provides only what diffah needs.
package registrytest

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/go-digest"
)

// Option configures the registrytest Server.
type Option func(*config)

type config struct {
	basicUser, basicPass string
	bearerToken          string
	tls                  bool
	caPEM                []byte
	certDir              string
	faults               []*faultRule
}

// WithBasicAuth enables HTTP Basic-auth middleware.
func WithBasicAuth(user, pass string) Option {
	return func(c *config) { c.basicUser, c.basicPass = user, pass }
}

// WithBearerToken enables Bearer-token middleware.
func WithBearerToken(token string) Option {
	return func(c *config) { c.bearerToken = token }
}

// BlobRequest records a single GET/HEAD for /v2/<repo>/blobs/<digest>.
type BlobRequest struct {
	Repo   string
	Digest digest.Digest
}

// ManifestRequest records a single GET/HEAD for /v2/<repo>/manifests/<reference>.
// Reference may be a tag (e.g. "v1") or a digest ("sha256:..."). Tracked so
// integration tests can budget how many times preflight + apply collectively
// pull a baseline manifest.
type ManifestRequest struct {
	Repo      string
	Reference string
	Method    string
}

// Server is the in-process registry returned by New.
type Server struct {
	httptest *httptest.Server
	caPEM    []byte
	certDir  string

	mu           sync.Mutex
	blobHits     []BlobRequest
	manifestHits []ManifestRequest
}

// New starts a fresh in-process registry and registers t.Cleanup to
// shut it down. Use Options to add middleware.
func New(t *testing.T, opts ...Option) *Server {
	t.Helper()
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	s := &Server{}
	base := registry.New()
	h := s.accessLogMiddleware(base)
	h = authMiddleware(cfg, h)
	h = faultMiddleware(cfg, h)

	if cfg.tls {
		cert := generateTLSMaterial(t, cfg)
		ts := httptest.NewUnstartedServer(h)
		ts.TLS = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		ts.StartTLS()
		s.httptest = ts
	} else {
		s.httptest = httptest.NewServer(h)
	}
	s.caPEM = cfg.caPEM
	s.certDir = cfg.certDir
	t.Cleanup(s.Close)
	return s
}

// URL returns the base URL of the test registry (e.g. http://127.0.0.1:XXXX).
func (s *Server) URL() string { return s.httptest.URL }

// Close tears down the underlying httptest.Server.
func (s *Server) Close() {
	if s.httptest != nil {
		s.httptest.Close()
		s.httptest = nil
	}
}

// CACertPEM returns the PEM-encoded server certificate (which doubles
// as the CA in this harness's self-signed chain). Empty if WithTLS
// was not passed.
func (s *Server) CACertPEM() []byte { return s.caPEM }

// ClientCertDir returns a directory suitable for --cert-dir, containing
// registry.crt. Empty if WithTLS was not passed.
func (s *Server) ClientCertDir() string { return s.certDir }

// BlobHits returns every /v2/<repo>/blobs/<digest> request observed.
// Tests use it to assert lazy-fetch behaviour.
func (s *Server) BlobHits() []BlobRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BlobRequest, len(s.blobHits))
	copy(out, s.blobHits)
	return out
}

// ManifestHits returns every /v2/<repo>/manifests/<reference> request
// observed. Used by preflight tests to assert that pre-flight does not
// regress baseline manifest GET counts.
func (s *Server) ManifestHits() []ManifestRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ManifestRequest, len(s.manifestHits))
	copy(out, s.manifestHits)
	return out
}

var (
	blobPathRegex     = regexp.MustCompile(`^/v2/(.+)/blobs/(sha256:[0-9a-f]+)$`)
	manifestPathRegex = regexp.MustCompile(`^/v2/(.+)/manifests/(.+)$`)
)

func (s *Server) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := blobPathRegex.FindStringSubmatch(r.URL.Path); m != nil {
			s.mu.Lock()
			s.blobHits = append(s.blobHits, BlobRequest{
				Repo: m[1], Digest: digest.Digest(m[2]),
			})
			s.mu.Unlock()
		} else if m := manifestPathRegex.FindStringSubmatch(r.URL.Path); m != nil {
			s.mu.Lock()
			s.manifestHits = append(s.manifestHits, ManifestRequest{
				Repo: m[1], Reference: m[2], Method: r.Method,
			})
			s.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

type faultRule struct {
	match   func(*http.Request) bool
	status  int
	failN   int
	counter int
}

// WithInjectFault makes the first failN matching requests return status.
// Use e.g. failN=2 to exercise a 3-retry loop that succeeds on attempt 3.
func WithInjectFault(match func(*http.Request) bool, status, failN int) Option {
	return func(c *config) {
		c.faults = append(c.faults, &faultRule{match: match, status: status, failN: failN})
	}
}

func faultMiddleware(cfg *config, next http.Handler) http.Handler {
	if len(cfg.faults) == 0 {
		return next
	}
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		for _, f := range cfg.faults {
			if f.counter < f.failN && f.match(r) {
				f.counter++
				mu.Unlock()
				http.Error(w, "injected fault", f.status)
				return
			}
		}
		mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func authMiddleware(cfg *config, next http.Handler) http.Handler {
	if cfg.basicUser == "" && cfg.bearerToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.basicUser != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != cfg.basicUser || pass != cfg.basicPass {
				w.Header().Set("WWW-Authenticate", `Basic realm="registrytest"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if cfg.bearerToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != cfg.bearerToken {
				w.Header().Set("WWW-Authenticate", `Bearer realm="registrytest"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
