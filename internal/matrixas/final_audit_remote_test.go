package matrixas

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/connector"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type blockedRemoteConnector struct {
	api                remoteapi.PuppetAPI
	started            chan struct{}
	canceled           chan struct{}
	releaseAfterCancel <-chan struct{}
	runs               atomic.Int32
}

func (conn *blockedRemoteConnector) API() remoteapi.PuppetAPI { return conn.api }

func (conn *blockedRemoteConnector) Run(ctx context.Context) error {
	conn.runs.Add(1)
	select {
	case conn.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case conn.canceled <- struct{}{}:
	default:
	}
	if conn.releaseAfterCancel != nil {
		<-conn.releaseAfterCancel
	}
	return ctx.Err()
}

func TestStartRemoteRunsCapturedConnectorAndGeneration(t *testing.T) {
	oldConn := &blockedRemoteConnector{api: &accountSessionAPI{}, started: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	newConn := &blockedRemoteConnector{api: &accountSessionAPI{}, started: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	beforeRun := make(chan struct{})
	releaseRun := make(chan struct{})
	s := &Server{conn: oldConn, beforeRemoteRun: func(uint64) { close(beforeRun); <-releaseRun }}

	s.startRemote(context.Background())
	select {
	case <-beforeRun:
	case <-time.After(time.Second):
		t.Fatal("remote generation did not reach launch barrier")
	}
	s.mu.Lock()
	s.conn = newConn
	s.mu.Unlock()
	close(releaseRun)
	select {
	case <-oldConn.started:
	case <-time.After(time.Second):
		t.Fatal("captured old connector did not run")
	}
	select {
	case <-newConn.started:
		t.Fatal("remote goroutine dereferenced mutable replacement connector")
	default:
	}
	if err := s.stopRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUseSavedAccountWaitsForOldGenerationBeforePublishingOrStartingReplacement(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	releaseOld := make(chan struct{})
	oldAPI := &accountSessionAPI{self: "wxid_old"}
	oldConn := &blockedRemoteConnector{api: oldAPI, started: make(chan struct{}, 1), canceled: make(chan struct{}, 1), releaseAfterCancel: releaseOld}
	newConn := &blockedRemoteConnector{api: &accountSessionAPI{self: "wxid_other"}, started: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	s := &Server{
		accounts: accounts,
		cfg:      &config.Config{Network: config.Network{Protocol: "wechat"}},
		conn:     oldConn,
		api:      oldAPI,
		newConnector: func(string, remoteapi.Config, connector.EventSink) (remoteConnector, error) {
			return newConn, nil
		},
		remoteStopTimeout: time.Second,
	}
	s.startRemote(context.Background())
	waitRemoteSignal(t, oldConn.started, "old connector start")

	transitionDone := make(chan error, 1)
	go func() { transitionDone <- s.useSavedAccount(context.Background(), "wxid_other") }()
	waitRemoteSignal(t, oldConn.canceled, "old connector cancellation")
	s.mu.Lock()
	published := s.conn
	s.mu.Unlock()
	if published != oldConn {
		t.Fatal("replacement connector published before old generation joined")
	}
	select {
	case <-newConn.started:
		t.Fatal("replacement connector started before old generation joined")
	default:
	}
	close(releaseOld)
	select {
	case err := <-transitionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("account transition did not finish after old generation stopped")
	}
	s.mu.Lock()
	published = s.conn
	s.mu.Unlock()
	if published != newConn {
		t.Fatal("replacement connector was not published after old generation joined")
	}
	s.startRemote(context.Background())
	waitRemoteSignal(t, newConn.started, "replacement connector start")
	if err := s.stopRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUseSavedAccountTransitionGapCannotRestartStoppedConnector(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	releaseOld := make(chan struct{})
	gapReached := make(chan struct{})
	releasePublish := make(chan struct{})
	oldAPI := &accountSessionAPI{self: "wxid_old"}
	oldConn := &blockedRemoteConnector{api: oldAPI, started: make(chan struct{}, 2), canceled: make(chan struct{}, 1), releaseAfterCancel: releaseOld}
	newAPI := &accountSessionAPI{self: "wxid_other"}
	newConn := &blockedRemoteConnector{api: newAPI, started: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	s := &Server{
		accounts:      accounts,
		cfg:           &config.Config{Network: config.Network{Protocol: "wechat"}},
		conn:          oldConn,
		api:           oldAPI,
		activeAccount: "wxid_old",
		newConnector: func(string, remoteapi.Config, connector.EventSink) (remoteConnector, error) {
			return newConn, nil
		},
		beforeRemotePublish: func() {
			close(gapReached)
			<-releasePublish
		},
		remoteStopTimeout: time.Second,
	}
	s.startRemote(context.Background())
	waitRemoteSignal(t, oldConn.started, "old connector start")

	transitionDone := make(chan error, 1)
	go func() { transitionDone <- s.useSavedAccount(context.Background(), "wxid_other") }()
	waitRemoteSignal(t, oldConn.canceled, "old connector cancellation")
	close(releaseOld)
	waitRemoteSignal(t, gapReached, "joined-stop/publish transition gap")

	s.startRemote(context.Background())
	s.mu.Lock()
	restarted := s.remoteRun
	transitioning := s.remoteTransition
	publishedConn := s.conn
	publishedAccount := s.activeAccount
	s.mu.Unlock()
	if restarted || oldConn.runs.Load() != 1 {
		t.Fatalf("stopped connector restarted in transition gap: remoteRun=%v runs=%d", restarted, oldConn.runs.Load())
	}
	if !transitioning || publishedConn != oldConn || publishedAccount != "wxid_old" {
		t.Fatalf("gap publication = transitioning=%v conn=%T account=%q", transitioning, publishedConn, publishedAccount)
	}

	close(releasePublish)
	select {
	case err := <-transitionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("account transition did not publish replacement")
	}
	s.mu.Lock()
	transitioning = s.remoteTransition
	publishedConn = s.conn
	publishedAccount = s.activeAccount
	s.mu.Unlock()
	if transitioning || publishedConn != newConn || publishedAccount != "wxid_other" {
		t.Fatalf("replacement publication = transitioning=%v conn=%T account=%q", transitioning, publishedConn, publishedAccount)
	}
	s.startRemote(context.Background())
	waitRemoteSignal(t, newConn.started, "replacement connector start")
	if err := s.stopRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUseSavedAccountStopTimeoutDoesNotReplaceRunningGeneration(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	releaseOld := make(chan struct{})
	oldAPI := &accountSessionAPI{self: "wxid_old"}
	oldConn := &blockedRemoteConnector{api: oldAPI, started: make(chan struct{}, 1), canceled: make(chan struct{}, 1), releaseAfterCancel: releaseOld}
	newConn := &blockedRemoteConnector{api: &accountSessionAPI{self: "wxid_other"}, started: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	s := &Server{
		accounts: accounts,
		cfg:      &config.Config{Network: config.Network{Protocol: "wechat"}},
		conn:     oldConn,
		api:      oldAPI,
		newConnector: func(string, remoteapi.Config, connector.EventSink) (remoteConnector, error) {
			return newConn, nil
		},
		remoteStopTimeout: 20 * time.Millisecond,
	}
	s.startRemote(context.Background())
	waitRemoteSignal(t, oldConn.started, "old connector start")

	err := s.useSavedAccount(context.Background(), "wxid_other")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transition error = %v, want stop deadline", err)
	}
	s.mu.Lock()
	published := s.conn
	running := s.remoteRun
	s.mu.Unlock()
	if published != oldConn || !running {
		t.Fatalf("timed-out stop replaced or cleared old generation: conn=%T running=%v", published, running)
	}
	if current, ok := accounts.current(); !ok || current.ID != "wxid_old" {
		t.Fatalf("durable current was not restored after stop timeout: %#v, %v", current, ok)
	}
	select {
	case <-newConn.started:
		t.Fatal("replacement started after old-generation stop timeout")
	default:
	}
	close(releaseOld)
	deadline := time.After(time.Second)
	for {
		s.mu.Lock()
		running = s.remoteRun
		s.mu.Unlock()
		if !running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("old generation did not clear after backend returned")
		case <-time.After(time.Millisecond):
		}
	}
}

func waitRemoteSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}
