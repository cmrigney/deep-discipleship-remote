// Command relay is the Deep Discipleship Remote relay server.
//
// Usage:
//
//	relay [serve]         run the server (configured via environment, see below)
//	relay hash-password   read a password from stdin and print its bcrypt hash
//
// Environment:
//
//	RELAY_LISTEN            listen address (default 127.0.0.1:8080)
//	RELAY_PASSWORD_HASH     bcrypt hash of the shared password (required unless RELAY_PASSWORD is set)
//	RELAY_PASSWORD          plaintext password, for local development only
//	RELAY_TRUST_CF_HEADERS  use CF-Connecting-IP as client IP (default true)
//	RELAY_ALLOWED_ORIGINS   comma-separated extra WebSocket Origin patterns
//	                        (default: the Safari, Chrome and Firefox extension schemes)
//	RELAY_LOG_FORMAT        "text" (default) or "json"
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"ddremote/relay/internal/auth"
	"ddremote/relay/internal/hub"
	"ddremote/relay/internal/server"
	"ddremote/relay/web"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "hash-password":
		err = hashPassword()
	case "-h", "--help", "help":
		fmt.Println("usage: relay [serve|hash-password]")
		return
	default:
		err = fmt.Errorf("unknown command %q (want serve or hash-password)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
}

func hashPassword() error {
	var pw string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, "Again: ")
		b2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if string(b) != string(b2) {
			return errors.New("passwords do not match")
		}
		pw = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if len(pw) < 8 {
		return errors.New("use at least 8 characters")
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	fmt.Println(string(h))
	return nil
}

func serve() error {
	var handler slog.Handler = slog.NewTextHandler(os.Stderr, nil)
	if os.Getenv("RELAY_LOG_FORMAT") == "json" {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	log := slog.New(handler)

	hash := []byte(os.Getenv("RELAY_PASSWORD_HASH"))
	if len(hash) == 0 {
		pw := os.Getenv("RELAY_PASSWORD")
		if pw == "" {
			return errors.New("set RELAY_PASSWORD_HASH (generate with `relay hash-password`)")
		}
		log.Warn("using plaintext RELAY_PASSWORD; use RELAY_PASSWORD_HASH in production")
		var err error
		if hash, err = auth.HashPassword(pw); err != nil {
			return err
		}
	}
	guard, err := auth.NewGuard(hash, auth.DefaultConfig())
	if err != nil {
		return fmt.Errorf("RELAY_PASSWORD_HASH is not a valid bcrypt hash: %w", err)
	}

	trustCF := true
	if v := os.Getenv("RELAY_TRUST_CF_HEADERS"); v != "" {
		if trustCF, err = strconv.ParseBool(v); err != nil {
			return fmt.Errorf("RELAY_TRUST_CF_HEADERS: %w", err)
		}
	}
	origins := server.DefaultAllowedOrigins
	if v := os.Getenv("RELAY_ALLOWED_ORIGINS"); v != "" {
		origins = nil
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
	}
	listen := os.Getenv("RELAY_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h := hub.New()
	go h.Run(ctx)

	srv := server.New(server.Config{
		Guard:          guard,
		Hub:            h,
		Static:         web.Static(),
		TrustCFHeaders: trustCF,
		AllowedOrigins: origins,
		Limits:         server.DefaultLimits(),
		Logger:         log,
	})
	go srv.RunJanitor(ctx, 5*time.Minute)

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("relay listening", "addr", listen, "trust_cf_headers", trustCF)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(sctx)
}
