package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/bcrypt"

	"ddremote/relay/internal/auth"
	"ddremote/relay/internal/hub"
)

const testPassword = "correct horse"

type env struct {
	t   *testing.T
	srv *httptest.Server
	url string
}

func newEnv(t *testing.T, mutate func(*Limits)) *env {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := auth.NewGuard(hash, auth.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := hub.New()
	go h.Run(ctx)

	limits := DefaultLimits()
	limits.HTTPBurst = 1000
	limits.HTTPPerSec = 1000
	limits.UpgradesPerMinute = 1000
	if mutate != nil {
		mutate(&limits)
	}
	s := New(Config{
		Guard:          guard,
		Hub:            h,
		Static:         fstest.MapFS{"index.html": {Data: []byte("<h1>hi</h1>")}},
		TrustCFHeaders: true,
		AllowedOrigins: DefaultAllowedOrigins,
		Limits:         limits,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, srv: ts, url: "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"}
}

type client struct {
	t    *testing.T
	conn *websocket.Conn
}

func (e *env) dial(header http.Header) (*client, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, e.url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	e.t.Cleanup(func() { conn.CloseNow() })
	return &client{t: e.t, conn: conn}, resp, nil
}

func (e *env) connect(role, password string) *client {
	e.t.Helper()
	c, _, err := e.dial(nil)
	if err != nil {
		e.t.Fatalf("dial: %v", err)
	}
	c.send(map[string]any{"type": "auth", "role": role, "password": password, "clientName": role})
	if m := c.recv(); m["type"] != "auth_ok" {
		e.t.Fatalf("auth: got %v", m)
	}
	return c
}

func (c *client) send(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *client) recv() map[string]any {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		c.t.Fatalf("decode %s: %v", b, err)
	}
	return m
}

// recvType reads until a message of the given type arrives.
func (c *client) recvType(typ string) map[string]any {
	c.t.Helper()
	for i := 0; i < 20; i++ {
		if m := c.recv(); m["type"] == typ {
			return m
		}
	}
	c.t.Fatalf("no %q message", typ)
	return nil
}

func TestRouting(t *testing.T) {
	e := newEnv(t, nil)
	player := e.connect("player", testPassword)
	phone := e.connect("controller", testPassword)

	p := phone.recvType("presence")
	if p["players"] != float64(1) || p["controllers"] != float64(1) {
		t.Fatalf("presence = %v", p)
	}

	phone.send(map[string]any{"type": "play", "id": "c1", "junk": true})
	cmd := player.recvType("play")
	if cmd["id"] != "c1" || cmd["junk"] != nil {
		t.Fatalf("command = %v", cmd)
	}

	player.send(map[string]any{"type": "ack", "id": "c1", "ok": true})
	if a := phone.recvType("ack"); a["id"] != "c1" || a["ok"] != true {
		t.Fatalf("ack = %v", a)
	}

	player.send(map[string]any{"type": "state", "videoFound": true, "paused": false, "currentTime": 3, "duration": 100})
	if s := phone.recvType("state"); s["videoFound"] != true {
		t.Fatalf("state = %v", s)
	}

	// A phone that connects later gets the cached state immediately.
	late := e.connect("controller", testPassword)
	if s := late.recvType("state"); s["currentTime"] != float64(3) {
		t.Fatalf("late state = %v", s)
	}

	// Controllers can't impersonate the player.
	phone.send(map[string]any{"type": "state", "videoFound": false})
	if m := phone.recvType("error"); m["reason"] != "invalid_message" {
		t.Fatalf("error = %v", m)
	}

	// App-level ping.
	phone.send(map[string]any{"type": "ping"})
	phone.recvType("pong")
}

func TestPlayerDisconnectClearsStateAndUpdatesPresence(t *testing.T) {
	e := newEnv(t, nil)
	player := e.connect("player", testPassword)
	phone := e.connect("controller", testPassword)
	phone.recvType("presence")
	player.send(map[string]any{"type": "state", "videoFound": true})
	phone.recvType("state")

	player.conn.Close(websocket.StatusNormalClosure, "")
	if p := phone.recvType("presence"); p["players"] != float64(0) {
		t.Fatalf("presence after disconnect = %v", p)
	}

	late := e.connect("controller", testPassword)
	if m := late.recv(); m["type"] != "presence" {
		t.Fatalf("expected presence (no stale state), got %v", m)
	}
}

func TestBadPasswordAndLockout(t *testing.T) {
	e := newEnv(t, nil)
	hdr := http.Header{"Cf-Connecting-Ip": {"203.0.113.9"}}
	for i := 0; i < 5; i++ {
		c, _, err := e.dial(hdr)
		if err != nil {
			t.Fatal(err)
		}
		c.send(map[string]any{"type": "auth", "role": "controller", "password": "nope"})
		m := c.recv()
		want := "bad_password"
		if i == 4 {
			want = "locked_out"
		}
		if m["type"] != "auth_error" || m["reason"] != want {
			t.Fatalf("attempt %d: %v", i, m)
		}
		c.conn.CloseNow()
	}
	c, _, err := e.dial(hdr)
	if err != nil {
		t.Fatal(err)
	}
	c.send(map[string]any{"type": "auth", "role": "controller", "password": testPassword})
	if m := c.recv(); m["reason"] != "locked_out" {
		t.Fatalf("correct password while locked: %v", m)
	}
	// Different IP is fine.
	ok, _, err := e.dial(http.Header{"Cf-Connecting-Ip": {"198.51.100.1"}})
	if err != nil {
		t.Fatal(err)
	}
	ok.send(map[string]any{"type": "auth", "role": "controller", "password": testPassword})
	if m := ok.recv(); m["type"] != "auth_ok" {
		t.Fatalf("other ip: %v", m)
	}
}

func TestAuthTimeout(t *testing.T) {
	e := newEnv(t, func(l *Limits) { l.AuthTimeout = 200 * time.Millisecond })
	c, _, err := e.dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := c.recv(); m["reason"] != "timeout" {
		t.Fatalf("got %v", m)
	}
}

func TestFirstMessageMustBeAuth(t *testing.T) {
	e := newEnv(t, nil)
	c, _, err := e.dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.send(map[string]any{"type": "play"})
	if m := c.recv(); m["type"] != "auth_error" {
		t.Fatalf("got %v", m)
	}
}

func TestMessageRateLimitDisconnects(t *testing.T) {
	e := newEnv(t, func(l *Limits) { l.ControllerPerSec = 0.001; l.ControllerBurst = 2 })
	phone := e.connect("controller", testPassword)
	for i := 0; i < 6; i++ {
		b, _ := json.Marshal(map[string]any{"type": "ping"})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = phone.conn.Write(ctx, websocket.MessageText, b)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		_, _, err := phone.conn.Read(ctx)
		if err == nil {
			continue // rate_limited error replies may or may not arrive before the close
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation || ce.Reason != "rate_limited" {
			t.Fatalf("expected rate_limited policy close, got %v", err)
		}
		return
	}
}

func TestOriginCheck(t *testing.T) {
	e := newEnv(t, nil)
	if _, resp, err := e.dial(http.Header{"Origin": {"https://evil.example"}}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin: err=%v resp=%v", err, resp)
	}
	for _, o := range []string{"safari-web-extension://ABCD-1234", "chrome-extension://abcdef", "moz-extension://1234"} {
		if _, _, err := e.dial(http.Header{"Origin": {o}}); err != nil {
			t.Fatalf("origin %s rejected: %v", o, err)
		}
	}
}

func TestConnectionCapPerIP(t *testing.T) {
	e := newEnv(t, func(l *Limits) { l.MaxConnsPerIP = 2 })
	hdr := http.Header{"Cf-Connecting-Ip": {"192.0.2.7"}}
	for i := 0; i < 2; i++ {
		if _, _, err := e.dial(hdr); err != nil {
			t.Fatal(err)
		}
	}
	if _, resp, err := e.dial(hdr); err == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("3rd connection: err=%v", err)
	}
}

func TestHTTPRateLimitAndHeaders(t *testing.T) {
	e := newEnv(t, func(l *Limits) { l.HTTPPerSec = 0.001; l.HTTPBurst = 2 })
	get := func() *http.Response {
		req, _ := http.NewRequest("GET", e.srv.URL+"/", nil)
		req.Header.Set("CF-Connecting-IP", "192.0.2.50")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	r := get()
	if r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("csp = %q", csp)
	}
	get()
	if r := get(); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("3rd request status %d", r.StatusCode)
	}
	// Health check is never limited.
	resp, err := http.Get(e.srv.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, resp)
	}
}
