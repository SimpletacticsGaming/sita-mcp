package sita_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestErrors(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
		want error
	}{
		{"forbidden", func() error { _, err := c.Entry(ctx, bob, 5); return err }, sita.ErrForbidden},
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

func TestRevokedTokenIsRejected(t *testing.T) {
	b := sitatest.New(t)
	c := newClient(b)
	ctx := context.Background()

	if _, err := c.Topic(ctx, alice, 0); err != nil {
		t.Fatal(err)
	}
	b.Revoke(sitatest.AliceToken)
	if _, err := c.Topic(ctx, alice, 0); !errors.Is(err, sita.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
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

	info, err := c.Verify(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if info.Username != "bob" {
		t.Fatalf("info = %+v", info)
	}
	if _, err := c.Verify(ctx, sita.Credentials{Token: "bogus"}); !errors.Is(err, sita.ErrUnauthorized) {
		t.Fatalf("bogus token: %v", err)
	}
}

func TestVerifyTreatsForbiddenAsUnauthorized(t *testing.T) {
	// Spring Security answers unauthenticated requests with 403 unless configured otherwise.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := sita.NewClient(srv.URL, srv.Client()).Verify(context.Background(), alice)
	if !errors.Is(err, sita.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}
