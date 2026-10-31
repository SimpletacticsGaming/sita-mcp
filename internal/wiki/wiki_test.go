package wiki_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
	"github.com/SimpletacticsGaming/sita-mcp/internal/sita/sitatest"
	"github.com/SimpletacticsGaming/sita-mcp/internal/wiki"
)

var (
	alice = sita.Credentials{Token: sitatest.AliceToken}
	bob   = sita.Credentials{Token: sitatest.BobToken}
)

func newService(t *testing.T) (*wiki.Service, *sitatest.Backend) {
	t.Helper()
	b := sitatest.New(t)
	client := sita.NewClient(b.URL, b.Client())
	return wiki.NewService(client, wiki.Options{PublicURL: "https://example.test/wiki/?ar=", IndexTTL: time.Minute, MaxNodes: 100}), b
}

func TestPageEntry(t *testing.T) {
	svc, _ := newService(t)
	page, err := svc.Page(context.Background(), alice, 3)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Kampfregeln" || page.Type != sita.NodeEntry {
		t.Fatalf("page = %+v", page)
	}
	if !strings.Contains(page.Content, "**W20**") || !strings.Contains(page.Content, "## Initiative") {
		t.Fatalf("content not converted to Markdown: %q", page.Content)
	}
	if len(page.Breadcrumb) != 2 || page.Breadcrumb[0].Title != "Startseite" || page.Breadcrumb[1].Title != "Regeln" {
		t.Fatalf("breadcrumb = %+v", page.Breadcrumb)
	}
	if page.URL != "https://example.test/wiki/?ar=3" {
		t.Fatalf("url = %q", page.URL)
	}
}

func TestPageTopicHidesForbiddenChildren(t *testing.T) {
	svc, _ := newService(t)
	page, err := svc.Page(context.Background(), bob, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Startseite" || len(page.Breadcrumb) != 0 {
		t.Fatalf("page = %+v", page)
	}
	if len(page.Children) != 1 || page.Children[0].Title != "Regeln" {
		t.Fatalf("children = %+v", page.Children)
	}
}

func TestPagePolls(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	open, err := svc.Page(ctx, alice, 4)
	if err != nil {
		t.Fatal(err)
	}
	if open.Title != "Nächster Spieleabend?" || open.Poll == nil || open.Poll.Closed || len(open.Poll.Options) != 2 || !open.Poll.Options[0].OwnVoteYes {
		t.Fatalf("open poll = %+v %+v", open, open.Poll)
	}

	closed, err := svc.Page(ctx, alice, 6)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Title != "Alte Umfrage" || closed.Poll == nil || !closed.Poll.Closed {
		t.Fatalf("closed poll = %+v %+v", closed, closed.Poll)
	}
}

func TestTree(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	tree, err := svc.Tree(ctx, alice, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := `- [0] Startseite (TOPIC)
  - [1] Regeln (TOPIC)
    - [3] Kampfregeln (ENTRY)
    - [4] Nächster Spieleabend? (POLL)
  - [2] Kampagnen (TOPIC)
    - [5] Geheime Kampagne (ENTRY)
    - [6] Alte Umfrage (POLL)
`
	if tree.Outline != want {
		t.Fatalf("outline =\n%s\nwant\n%s", tree.Outline, want)
	}

	shallow, err := svc.Tree(ctx, bob, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if shallow.Outline != "- [0] Startseite (TOPIC)\n  - [1] Regeln (TOPIC)\n" {
		t.Fatalf("bob depth 1 =\n%s", shallow.Outline)
	}

	if _, err := svc.Tree(ctx, alice, 3, 1); err == nil {
		t.Fatal("tree of an entry should fail")
	}
}

func TestSearch(t *testing.T) {
	svc, b := newService(t)
	ctx := context.Background()

	res, err := svc.Search(ctx, alice, "w20 initiative", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Results[0].ID != 3 || res.Partial {
		t.Fatalf("results = %+v", res)
	}
	if res.Results[0].Breadcrumb != "Startseite › Regeln" || !strings.Contains(res.Results[0].Snippet, "W20") {
		t.Fatalf("hit = %+v", res.Results[0])
	}

	// Title matches rank above content matches; words matched only partly fall back to OR.
	res, err = svc.Search(ctx, alice, "Drache Kampagne", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) == 0 || res.Results[0].ID != 5 {
		t.Fatalf("results = %+v", res)
	}

	// The index is cached per user.
	before := b.Requests()
	if _, err := svc.Search(ctx, alice, "Regeln", 10); err != nil {
		t.Fatal(err)
	}
	if b.Requests() != before {
		t.Fatalf("second search hit the backend %d times", b.Requests()-before)
	}

	// Bob must not find what he may not read.
	res, err = svc.Search(ctx, bob, "Drache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 0 {
		t.Fatalf("bob found hidden content: %+v", res.Results)
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Search(context.Background(), alice, "  ", 10); err == nil {
		t.Fatal("expected error")
	}
}
