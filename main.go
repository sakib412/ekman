package ekman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// shutdownTimeout bounds how long in-flight requests get after SIGINT/SIGTERM.
const shutdownTimeout = 30 * time.Second

// Main runs the command named by os.Args[1]: "serve" (default), "migrate" or "openapi" (writes
// the document to stdout). SIGINT and SIGTERM shut the server down gracefully. It exits the
// process with status 1 on error and 2 on an unknown command.
func (a *App) Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := a.run(ctx, os.Args[1:], os.Stdout)
	if err == nil {
		return
	}
	var u usageError
	if errors.As(err, &u) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	a.log.Error("ekman: "+err.Error(), "err", err.Error())
	os.Exit(1)
}

type usageError string

func (u usageError) Error() string { return string(u) }

func (a *App) run(ctx context.Context, args []string, stdout io.Writer) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		h, err := a.Handler()
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", a.opts.Addr)
		if err != nil {
			return err
		}
		return a.serve(ctx, ln, h)
	case "migrate":
		if err := a.Migrate(ctx); err != nil {
			return err
		}
		a.log.Info("ekman: migrations applied")
		return nil
	case "openapi":
		doc, err := a.OpenAPI()
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(doc, '\n'))
		return err
	}
	return usageError(fmt.Sprintf("ekman: unknown command %q (want serve, migrate or openapi)", cmd))
}

func (a *App) serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	a.log.Info("ekman: listening", "addr", ln.Addr().String())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	a.log.Info("ekman: shutting down")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
