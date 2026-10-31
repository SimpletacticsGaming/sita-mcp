package sita_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
	"github.com/SimpletacticsGaming/sita-mcp/internal/sita/sitatest"
)

var alice = sita.Credentials{Username: "alice", Password: "secret"}

func newClient(b *sitatest.Backend) *sita.Client {
	return sita.NewClient(b.URL, "authentication", b.Client())
}

func TestLoginIsCachedAcrossCalls(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := c.Topic(ctx, alice, 0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if got := b.Logins(); got != 1 {
		t.Fatalf("logins = %d, want 1", got)
	}
}

func TestReloginAfterSessionExpired(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	if _, err := c.Topic(ctx, alice, 0); err != nil {
		t.Fatal(err)
	}
	b.ExpireSessions()
	topic, err := c.Topic(ctx, alice, 1)
	if err != nil {
		t.Fatal(err)
	}
	if topic.Topic != "Regeln" {
		t.Fatalf("topic = %q", topic.Topic)
	}
	if got := b.Logins(); got != 2 {
		t.Fatalf("logins = %d, want 2", got)
	}
}

func TestErrors(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()
	bob := sita.Credentials{Username: "bob", Password: "hunter2"}

	tests := []struct {
		name  string
		creds sita.Credentials
		call  func(sita.Credentials) error
		want  error
	}{
		{"wrong password", sita.Credentials{Username: "alice", Password: "nope"}, func(cr sita.Credentials) error { _, err := c.Topic(ctx, cr, 0); return err }, sita.ErrLoginFailed},
		{"forbidden", bob, func(cr sita.Credentials) error { _, err := c.Entry(ctx, cr, 5); return err }, sita.ErrForbidden},
		{"not found", alice, func(cr sita.Credentials) error { _, err := c.Entry(ctx, cr, 99); return err }, sita.ErrNotFound},
		{"unknown type", alice, func(cr sita.Credentials) error { _, err := c.NodeType(ctx, cr, 99); return err }, sita.ErrNotFound},
		{"closed poll", alice, func(cr sita.Credentials) error { _, err := c.Poll(ctx, cr, 6); return err }, sita.ErrPollClosed},
		{"invalid session", sita.Credentials{Session: "bogus"}, func(cr sita.Credentials) error { _, err := c.Topic(ctx, cr, 0); return err }, sita.ErrUnauthorized},
		{"no credentials", sita.Credentials{}, func(cr sita.Credentials) error { _, err := c.Topic(ctx, cr, 0); return err }, sita.ErrUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(tt.creds); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParentAndChildren(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	parent, err := c.Parent(ctx, alice, 3)
	if err != nil {
		t.Fatal(err)
	}
	if parent.ID != 1 || parent.Title != "Regeln" {
		t.Fatalf("parent = %+v", parent)
	}
	if _, err := c.Parent(ctx, alice, 0); !errors.Is(err, sita.ErrNotFound) {
		t.Fatalf("root parent err = %v", err)
	}

	children, err := c.Children(ctx, sita.Credentials{Username: "bob", Password: "hunter2"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ID != 1 {
		t.Fatalf("bob sees %+v, want only node 1", children)
	}
}

func TestVerify(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	if err := c.Verify(ctx, alice); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if err := c.Verify(ctx, sita.Credentials{Session: b.Session("bob")}); err != nil {
		t.Fatalf("bob session: %v", err)
	}
	if err := c.Verify(ctx, sita.Credentials{Session: "bogus"}); !errors.Is(err, sita.ErrUnauthorized) {
		t.Fatalf("bogus session: %v", err)
	}
	if err := c.Verify(ctx, sita.Credentials{Username: "alice", Password: "x"}); !errors.Is(err, sita.ErrLoginFailed) {
		t.Fatalf("wrong password: %v", err)
	}
}
