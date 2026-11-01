package ekman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRunOpenAPIAndUnknown(t *testing.T) {
	a, _ := newApp(t, Options{})
	var out bytes.Buffer
	if err := a.run(context.Background(), []string{"openapi"}, &out); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal("openapi output is not JSON")
	}
	var u usageError
	if err := a.run(context.Background(), []string{"bogus"}, &out); !errors.As(err, &u) {
		t.Fatalf("unknown command: %v", err)
	}
	if err := a.run(context.Background(), []string{"migrate"}, &out); err != nil {
		t.Fatalf("migrate with nothing to do: %v", err)
	}
}

func TestMigrateWithoutMigrationsNeedsNoDB(t *testing.T) {
	a, err := New(Options{Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	a.Register(Module{Name: "m"})
	if err := a.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	a, h := newApp(t, Options{}, Module{Name: "m", Routes: func(r *Router) {
		Handle(r, "GET /slow", Public(), func(context.Context, Caller, *struct{}) (item, error) {
			close(started)
			<-release
			return item{"done"}, nil
		})
	}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- a.serve(ctx, ln, h) }()

	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", resp, err)
	}
	resp.Body.Close()

	slow := make(chan string, 1)
	go func() {
		resp, err := http.Get(base + "/api/v1/slow")
		if err != nil {
			slow <- err.Error()
			return
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		slow <- b.String()
	}()
	<-started
	cancel() // like SIGTERM while a request is in flight
	time.Sleep(50 * time.Millisecond)
	close(release)
	if got := <-slow; !strings.Contains(got, "done") {
		t.Fatalf("in-flight request was not drained: %q", got)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
}
