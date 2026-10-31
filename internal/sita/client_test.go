package sita_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
	"github.com/SimpletacticsGaming/sita-mcp/internal/sita/sitatest"
)

var (
	alice = sita.Credentials{Token: sitatest.AliceToken}
	bob   = sita.Credentials{Token: sitatest.BobToken}
)

func newClient(b *sitatest.Backend) *sita.Client {
	return sita.NewClient(b.URL, b.Client())
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

func TestForbiddenNodeDoesNotTriggerRelogin(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)

	if _, err := c.Entry(context.Background(), bob, 5); !errors.Is(err, sita.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if got := b.Logins(); got != 1 {
		t.Fatalf("logins = %d, want 1", got)
	}
}

func TestRevokedTokenIsRejected(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	if _, err := c.Topic(ctx, alice, 0); err != nil {
		t.Fatal(err)
	}
	// Revoking deletes the token and its sessions; the re-login then fails.
	b.Revoke(sitatest.AliceToken)
	if _, err := c.Topic(ctx, alice, 0); !errors.Is(err, sita.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestErrors(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
		want error
	}{
		{"not found", func() error { _, err := c.Entry(ctx, alice, 99); return err }, sita.ErrNotFound},
		{"unknown type", func() error { _, err := c.NodeType(ctx, alice, 99); return err }, sita.ErrNotFound},
		{"closed poll", func() error { _, err := c.Poll(ctx, alice, 6); return err }, sita.ErrPollClosed},
		{"invalid token", func() error { _, err := c.Topic(ctx, sita.Credentials{Token: "bogus"}, 0); return err }, sita.ErrUnauthorized},
		{"no token", func() error { _, err := c.Topic(ctx, sita.Credentials{}, 0); return err }, sita.ErrUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, tt.want) {
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

	children, err := c.Children(ctx, bob, 0)
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

	if err := c.Verify(ctx, bob); err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(ctx, bob); err != nil || b.Logins() != 1 {
		t.Fatalf("second verify: err %v, logins %d, want cached session", err, b.Logins())
	}
	if err := c.Verify(ctx, sita.Credentials{Token: "bogus"}); !errors.Is(err, sita.ErrUnauthorized) {
		t.Fatalf("bogus token: %v", err)
	}
}
