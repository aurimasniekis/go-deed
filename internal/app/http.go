package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aurimasniekis/go-deed/internal/buildinfo"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
	"github.com/aurimasniekis/go-deed/internal/httpx"
	"github.com/aurimasniekis/go-deed/internal/webui"
)

// route classifies a path for metrics (bounded label values).
func route(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/sources/") && (strings.Contains(path, "/snapshots") || strings.HasSuffix(path, "/items") ||
		strings.HasSuffix(path, "/observations") || strings.HasSuffix(path, "/heartbeat") || strings.HasSuffix(path, "/terraform/state")):
		return "ingest"
	case strings.HasPrefix(path, "/api/v1/stream"):
		return "stream"
	case strings.HasPrefix(path, "/api/v1/graph"):
		return "graph"
	case strings.HasPrefix(path, "/api/v1/auth"), strings.HasPrefix(path, "/auth/"):
		return "auth"
	case strings.HasPrefix(path, "/api/"):
		return "api"
	case strings.HasPrefix(path, "/assets/"):
		return "assets"
	default:
		return "ui"
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// accessLog writes ECS-style access records (never query strings, bodies or credentials).
func (a *App) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		d := time.Since(start)
		rt := route(r.URL.Path)
		a.Metrics.HTTPRequest(rt, r.Method, sw.status, d)
		lvl := slog.LevelDebug
		if a.Cfg.Log.AccessLog {
			lvl = slog.LevelInfo
		}
		if sw.status >= 500 {
			lvl = slog.LevelWarn
		}
		a.Log.Log(r.Context(), lvl, "http request", "event.action", "http.request", "http.request.method", r.Method, "url.path", r.URL.Path,
			"http.response.status_code", sw.status, "event.duration_ms", d.Milliseconds(), "source.ip", httpx.ClientIP(r.Context()).String(),
			"http.request.id", httpx.GetRequestID(r.Context()), "http.route", rt)
	})
}

// APIDeps returns the API dependencies (also used by the openapi command with a nil app).
func (a *App) APIDeps() *httpapi.Deps {
	d := &httpapi.Deps{Log: a.Log, Version: buildinfo.Get().Version, Origin: a.Cfg.PublicOrigin(), Secure: a.Secure(), Store: a.Store,
		Keyring: a.Keyring, Audit: a.Audit, Authn: a.Authn, OIDC: a.OIDC, Catalog: a.Catalog, Metrics: a.Metrics, Clock: a.Clock,
		LoginRate: httpx.NewLimiter(a.Clock, 20, 10), SetupTTL: a.Cfg.Auth.SetupTokenTTL.D(), LocalAuth: a.Cfg.Auth.Local.Enabled, Ready: a.Ready}
	d.Extensions = append(d.Extensions, a.apiExt...)
	d.Raw = append(d.Raw, a.rawExt...)
	return d
}

// PublicHandler is the handler for the public listener.
func (a *App) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	httpapi.New(mux, a.APIDeps())
	mux.Handle("/", webui.Handler(webui.Dist(), nil))
	var h http.Handler = mux
	h = a.limitBody(h)
	h = httpx.SecurityHeaders(a.Secure(), h)
	h = a.accessLog(h)
	h = a.ClientIP.Middleware(h)
	return httpx.RequestID(h)
}

// limitBody caps request bodies; ingest and Terraform state uploads have larger limits enforced by their handlers.
func (a *App) limitBody(next http.Handler) http.Handler {
	small := httpx.LimitBody(a.Cfg.HTTP.MaxBodyBytes, next)
	large := httpx.LimitBody(max(a.Cfg.Limits.StateMaxBytes, a.Cfg.Limits.IngestMaxBytes)+(1<<20), next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route(r.URL.Path) == "ingest" || strings.HasPrefix(r.URL.Path, "/api/v1/icons") {
			large.ServeHTTP(w, r)
			return
		}
		small.ServeHTTP(w, r)
	})
}

// ManagementHandler serves metrics, health and (opt-in) pprof on the private listener.
func (a *App) ManagementHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", a.Metrics.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		err := a.Ready(r.Context())
		a.Metrics.SetReady(err == nil)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "not ready: %v\n", err)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	if a.Cfg.Management.Pprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}

func (a *App) tlsConfig() (*tls.Config, error) {
	c := a.Cfg.HTTP
	if c.TLSCertFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	if c.ClientCAFile != "" {
		pem, err := os.ReadFile(c.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("client CA file contains no certificates")
		}
		tc.ClientCAs = pool
		tc.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tc, nil
}

// Listeners are bound sockets (tests bind :0 and read the addresses).
type Listeners struct {
	Public, Management net.Listener
}

// Listen binds both listeners.
func (a *App) Listen(ctx context.Context) (*Listeners, error) {
	var lc net.ListenConfig
	pub, err := lc.Listen(ctx, "tcp", a.Cfg.HTTP.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", a.Cfg.HTTP.Listen, err)
	}
	mgmt, err := lc.Listen(ctx, "tcp", a.Cfg.Management.Listen)
	if err != nil {
		_ = pub.Close()
		return nil, fmt.Errorf("listen %s: %w", a.Cfg.Management.Listen, err)
	}
	return &Listeners{Public: pub, Management: mgmt}, nil
}

// Serve runs both servers and the background loops until ctx is canceled, then shuts down gracefully:
// readiness off → HTTP shutdown (15 s) → loops stop → (caller) store close.
func (a *App) Serve(ctx context.Context, ls *Listeners) error {
	tc, err := a.tlsConfig()
	if err != nil {
		return err
	}
	c := a.Cfg.HTTP
	public := &http.Server{Handler: a.PublicHandler(), ReadHeaderTimeout: c.ReadHeaderTimeout.D(), ReadTimeout: c.ReadTimeout.D(), //nolint:contextcheck // per-request contexts
		WriteTimeout: c.WriteTimeout.D(), IdleTimeout: c.IdleTimeout.D(), MaxHeaderBytes: c.MaxHeaderBytes, TLSConfig: tc,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	mgmt := &http.Server{Handler: a.ManagementHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		if tc != nil {
			err = public.ServeTLS(ls.Public, "", "")
		} else {
			err = public.Serve(ls.Public)
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	g.Go(func() error {
		if err := mgmt.Serve(ls.Management); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	loopCtx, stopLoops := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		lg, lctx := errgroup.WithContext(loopCtx)
		for _, l := range a.loops {
			lg.Go(func() error {
				l.run(lctx)
				return nil
			})
		}
		_ = lg.Wait()
	}()
	g.Go(func() error {
		<-gctx.Done()
		a.Metrics.SetReady(false)
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		err := errors.Join(public.Shutdown(sctx), mgmt.Shutdown(sctx))
		stopLoops()
		select {
		case <-done:
		case <-sctx.Done():
			a.Log.Warn("background jobs did not stop within the shutdown timeout")
		}
		return err
	})
	a.Log.Info("deed serving", "event.action", "server.start", "public", ls.Public.Addr().String(), "management", ls.Management.Addr().String(),
		"public_url", a.Cfg.PublicURL, "tls", tc != nil, "client_cert_required", tc != nil && tc.ClientAuth == tls.RequireAndVerifyClientCert,
		"version", buildinfo.Get().Version)
	err = g.Wait()
	stopLoops()
	<-done
	return err
}
