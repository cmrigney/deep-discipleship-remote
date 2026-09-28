// Package server wires HTTP handlers, the WebSocket endpoint, auth and rate limits.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"ddremote/relay/internal/auth"
	"ddremote/relay/internal/hub"
	"ddremote/relay/internal/protocol"
	"ddremote/relay/internal/ratelimit"
)

// Limits holds all rate-limit knobs (PLAN.md §5.5).
type Limits struct {
	HTTPPerSec        rate.Limit
	HTTPBurst         int
	UpgradesPerMinute int
	MaxConnsPerIP     int
	MaxConnsTotal     int
	ControllerPerSec  rate.Limit
	ControllerBurst   int
	PlayerPerSec      rate.Limit
	PlayerBurst       int
	MaxViolations     int
	AuthTimeout       time.Duration
	WriteTimeout      time.Duration
	PingInterval      time.Duration
}

// DefaultLimits matches PLAN.md.
func DefaultLimits() Limits {
	return Limits{
		HTTPPerSec:        10,
		HTTPBurst:         50,
		UpgradesPerMinute: 20,
		MaxConnsPerIP:     10,
		MaxConnsTotal:     50,
		ControllerPerSec:  10,
		ControllerBurst:   20,
		PlayerPerSec:      20,
		PlayerBurst:       40,
		MaxViolations:     3,
		AuthTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		PingInterval:      25 * time.Second,
	}
}

// Config configures a Server.
type Config struct {
	Guard *auth.Guard
	Hub   *hub.Hub
	// Static is the phone UI file system (index.html at its root).
	Static fs.FS
	// TrustCFHeaders uses CF-Connecting-IP as the client IP. Only safe when the
	// server is reachable exclusively through cloudflared.
	TrustCFHeaders bool
	// AllowedOrigins are extra WebSocket Origin patterns (path.Match syntax on
	// "scheme://host"). The page's own origin is always allowed.
	AllowedOrigins []string
	Limits         Limits
	Logger         *slog.Logger
}

// DefaultAllowedOrigins covers the three browsers' extension schemes.
var DefaultAllowedOrigins = []string{
	"safari-web-extension://*",
	"chrome-extension://*",
	"moz-extension://*",
}

// Server is the relay's HTTP handler.
type Server struct {
	cfg     Config
	httpLim *ratelimit.KeyedLimiter
	upLim   *ratelimit.KeyedLimiter
	conns   *ratelimit.ConnCounter
	log     *slog.Logger
	static  http.Handler
}

// New creates a Server.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	l := cfg.Limits
	return &Server{
		cfg:     cfg,
		httpLim: ratelimit.NewKeyed(l.HTTPPerSec, l.HTTPBurst),
		upLim:   ratelimit.NewKeyed(rate.Limit(float64(l.UpgradesPerMinute)/60), l.UpgradesPerMinute),
		conns:   ratelimit.NewConnCounter(l.MaxConnsPerIP, l.MaxConnsTotal),
		log:     cfg.Logger,
		static:  http.FileServerFS(cfg.Static),
	}
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /ws", s.limitHTTP(http.HandlerFunc(s.handleWS)))
	mux.Handle("GET /", s.limitHTTP(s.securityHeaders(s.static)))
	return mux
}

// RunJanitor prunes limiter and lockout state until ctx ends.
func (s *Server) RunJanitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.httpLim.Cleanup(10 * time.Minute)
			s.upLim.Cleanup(10 * time.Minute)
			s.cfg.Guard.Cleanup()
		}
	}
}

// ClientIP returns the client address for r.
func (s *Server) ClientIP(r *http.Request) string {
	if s.cfg.TrustCFHeaders {
		if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" && net.ParseIP(ip) != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) limitHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.httpLim.Allow(s.ClientIP(r)) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Older Safari doesn't treat 'self' as covering ws(s)://, so list the host explicitly.
		host := r.Host
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; "+
			"connect-src 'self' wss://"+host+" ws://"+host+"; "+
			"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// originAllowed reports whether a WebSocket upgrade's Origin is acceptable.
// Browsers always send Origin; non-browser clients may omit it, and the
// password is the real control for those.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	o := strings.ToLower(u.Scheme + "://" + u.Host)
	for _, p := range s.cfg.AllowedOrigins {
		if ok, _ := path.Match(strings.ToLower(p), o); ok {
			return true
		}
	}
	return false
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ip := s.ClientIP(r)
	log := s.log.With("ip", ip)

	if !s.originAllowed(r) {
		log.Warn("ws rejected: origin", "origin", r.Header.Get("Origin"))
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if !s.upLim.Allow(ip) {
		log.Warn("ws rejected: upgrade rate")
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many connection attempts", http.StatusTooManyRequests)
		return
	}
	release, err := s.conns.Acquire(ip)
	if err != nil {
		status := http.StatusTooManyRequests
		if errors.Is(err, ratelimit.ErrTooManyTotal) {
			status = http.StatusServiceUnavailable
		}
		log.Warn("ws rejected: connections", "err", err)
		http.Error(w, err.Error(), status)
		return
	}
	defer release()

	// Origin was checked above against our own rules.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		log.Warn("ws accept failed", "err", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(protocol.MaxMessageBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	a, ok := s.authenticate(ctx, conn, ip, log)
	if !ok {
		return
	}
	log = log.With("role", a.Role, "client", a.ClientName)
	log.Info("client connected")
	defer log.Info("client disconnected")

	var kickOnce sync.Once
	kick := func() {
		kickOnce.Do(func() {
			cancel()
			go conn.Close(websocket.StatusTryAgainLater, "disconnected by server")
		})
	}
	c := hub.NewClient(a.Role, a.ClientName, ip, kick)
	if !s.cfg.Hub.Register(ctx, c) {
		return
	}
	// Use a fresh context: ctx may already be cancelled when we get here.
	defer func() {
		uctx, ucancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ucancel()
		s.cfg.Hub.Unregister(uctx, c)
	}()

	go s.writeLoop(ctx, conn, c, kick)
	go s.pingLoop(ctx, conn, kick)
	s.readLoop(ctx, conn, c, log)
}

func (s *Server) authenticate(ctx context.Context, conn *websocket.Conn, ip string, log *slog.Logger) (protocol.Auth, bool) {
	reject := func(reason string) (protocol.Auth, bool) {
		wctx, cancel := context.WithTimeout(ctx, s.cfg.Limits.WriteTimeout)
		defer cancel()
		_ = conn.Write(wctx, websocket.MessageText, protocol.Encode(protocol.ServerMessage{Type: protocol.TypeAuthError, Reason: reason}))
		conn.Close(websocket.StatusPolicyViolation, reason)
		return protocol.Auth{}, false
	}

	// An expired Read context closes the connection in coder/websocket, so the
	// timeout is enforced by a timer that can still tell the client why.
	timedOut := make(chan struct{})
	timer := time.AfterFunc(s.cfg.Limits.AuthTimeout, func() {
		close(timedOut)
		log.Info("auth timeout")
		reject("timeout")
	})
	typ, data, err := conn.Read(ctx)
	if !timer.Stop() {
		<-timedOut
		return protocol.Auth{}, false
	}
	if err != nil {
		return protocol.Auth{}, false
	}
	actx, cancel := context.WithTimeout(ctx, s.cfg.Limits.AuthTimeout)
	defer cancel()
	if typ != websocket.MessageText {
		return reject("malformed")
	}
	a, err := protocol.ParseAuth(data)
	if err != nil {
		log.Info("auth malformed", "err", err)
		return reject("malformed")
	}
	switch err := s.cfg.Guard.Check(actx, ip, a.Password); {
	case err == nil:
	case errors.Is(err, auth.ErrLockedOut):
		log.Warn("auth rejected: locked out")
		return reject("locked_out")
	case errors.Is(err, auth.ErrBadPassword):
		log.Warn("auth failed", "role", a.Role)
		// Tell the client about a lockout this failure just triggered.
		if s.cfg.Guard.LockedFor(ip) > 0 {
			return reject("locked_out")
		}
		return reject("bad_password")
	default:
		return reject("timeout")
	}

	wctx, wcancel := context.WithTimeout(ctx, s.cfg.Limits.WriteTimeout)
	defer wcancel()
	if err := conn.Write(wctx, websocket.MessageText, protocol.Encode(protocol.ServerMessage{Type: protocol.TypeAuthOK})); err != nil {
		return protocol.Auth{}, false
	}
	return a, true
}

func (s *Server) readLoop(ctx context.Context, conn *websocket.Conn, c *hub.Client, log *slog.Logger) {
	lim := rate.NewLimiter(s.cfg.Limits.ControllerPerSec, s.cfg.Limits.ControllerBurst)
	if c.Role == protocol.RolePlayer {
		lim = rate.NewLimiter(s.cfg.Limits.PlayerPerSec, s.cfg.Limits.PlayerBurst)
	}
	violations := 0
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if !lim.Allow() {
			violations++
			log.Warn("message rate limited", "violations", violations)
			if violations >= s.cfg.Limits.MaxViolations {
				conn.Close(websocket.StatusPolicyViolation, "rate_limited")
				return
			}
			s.cfg.Hub.Reply(ctx, c, protocol.Encode(protocol.ServerMessage{Type: protocol.TypeError, Reason: "rate_limited"}))
			continue
		}
		violations = 0
		if typ != websocket.MessageText {
			s.cfg.Hub.Reply(ctx, c, protocol.Encode(protocol.ServerMessage{Type: protocol.TypeError, Reason: "invalid_message"}))
			continue
		}
		mtype, clean, err := protocol.Validate(c.Role, data)
		if err != nil {
			log.Info("invalid message", "err", err)
			s.cfg.Hub.Reply(ctx, c, protocol.Encode(protocol.ServerMessage{Type: protocol.TypeError, Reason: "invalid_message"}))
			continue
		}
		if mtype == protocol.TypePing {
			s.cfg.Hub.Reply(ctx, c, protocol.Encode(protocol.ServerMessage{Type: protocol.TypePong}))
			continue
		}
		s.cfg.Hub.Route(ctx, c, mtype, clean)
	}
}

func (s *Server) writeLoop(ctx context.Context, conn *websocket.Conn, c *hub.Client, kick func()) {
	for msg := range c.Send() {
		wctx, cancel := context.WithTimeout(ctx, s.cfg.Limits.WriteTimeout)
		err := conn.Write(wctx, websocket.MessageText, msg)
		cancel()
		if err != nil {
			kick()
			// Drain until the hub closes the channel so it never blocks on us.
			for range c.Send() {
			}
			return
		}
	}
}

// pingLoop sends WebSocket pings so Cloudflare (≈100 s idle timeout) keeps the
// connection open and dead peers are detected.
func (s *Server) pingLoop(ctx context.Context, conn *websocket.Conn, kick func()) {
	t := time.NewTicker(s.cfg.Limits.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 2*s.cfg.Limits.PingInterval)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				kick()
				return
			}
		}
	}
}
