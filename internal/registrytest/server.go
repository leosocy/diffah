// Package registrytest spins an in-process OCI distribution registry
// for diffah's integration tests. Wraps go-containerregistry's in-process
// registry with optional Basic-auth, bearer-token, TLS, fault-injection,
// and access-logging middleware. Provides only what diffah needs.
package registrytest

import (
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

// Server is the in-process registry returned by New.
type Server struct {
	httptest *httptest.Server

	mu       sync.Mutex
	blobHits []BlobRequest
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
	s.httptest = httptest.NewServer(h)
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

// BlobHits returns every /v2/<repo>/blobs/<digest> request observed.
// Tests use it to assert lazy-fetch behaviour.
func (s *Server) BlobHits() []BlobRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BlobRequest, len(s.blobHits))
	copy(out, s.blobHits)
	return out
}

var blobPathRegex = regexp.MustCompile(`^/v2/(.+)/blobs/(sha256:[0-9a-f]+)$`)

func (s *Server) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := blobPathRegex.FindStringSubmatch(r.URL.Path); m != nil {
			s.mu.Lock()
			s.blobHits = append(s.blobHits, BlobRequest{
				Repo: m[1], Digest: digest.Digest(m[2]),
			})
			s.mu.Unlock()
		}
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
