package mcpserver_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SimpletacticsGaming/sita-mcp/internal/mcpserver"
	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
	"github.com/SimpletacticsGaming/sita-mcp/internal/sita/sitatest"
	"github.com/SimpletacticsGaming/sita-mcp/internal/wiki"
)

func connect(t *testing.T, creds sita.Credentials) *mcp.ClientSession {
	t.Helper()
	b := sitatest.New(t)
	svc := wiki.NewService(sita.NewClient(b.URL, b.Client()), wiki.Options{
		PublicURL: "https://example.test/wiki/?ar=", IndexTTL: time.Minute, MaxNodes: 100,
	})
	server := mcpserver.New(svc, func(*mcp.CallToolRequest) (sita.Credentials, error) { return creds, nil }, "test")

	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolsAreListedAsReadOnly(t *testing.T) {
	s := connect(t, sita.Credentials{Token: sitatest.AliceToken})
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tool.Name)
		}
	}
	for _, want := range []string{"wiki_search", "wiki_get_page", "wiki_list_children", "wiki_get_tree"} {
		if !names[want] {
			t.Errorf("tool %s missing", want)
		}
	}
}

func TestToolCalls(t *testing.T) {
	s := connect(t, sita.Credentials{Token: sitatest.AliceToken})

	page := call(t, s, "wiki_get_page", map[string]any{"id": 3})
	if page.IsError || !strings.Contains(textOf(page), "# Kampfregeln") || !strings.Contains(textOf(page), "**W20**") {
		t.Fatalf("wiki_get_page:\n%s", textOf(page))
	}
	if page.StructuredContent == nil {
		t.Fatal("wiki_get_page: no structured content")
	}

	search := call(t, s, "wiki_search", map[string]any{"query": "drache"})
	if search.IsError || !strings.Contains(textOf(search), "[5] Geheime Kampagne") {
		t.Fatalf("wiki_search:\n%s", textOf(search))
	}

	children := call(t, s, "wiki_list_children", map[string]any{})
	if children.IsError || !strings.Contains(textOf(children), "[1] Regeln (TOPIC)") {
		t.Fatalf("wiki_list_children:\n%s", textOf(children))
	}

	tree := call(t, s, "wiki_get_tree", map[string]any{"max_depth": 1})
	if tree.IsError || strings.Contains(textOf(tree), "Kampfregeln") || !strings.Contains(textOf(tree), "[2] Kampagnen") {
		t.Fatalf("wiki_get_tree:\n%s", textOf(tree))
	}

	poll := call(t, s, "wiki_get_page", map[string]any{"id": 4})
	if !strings.Contains(textOf(poll), "- Samstag (yes)") {
		t.Fatalf("poll:\n%s", textOf(poll))
	}
}

func TestToolErrors(t *testing.T) {
	bob := connect(t, sita.Credentials{Token: sitatest.BobToken})
	res := call(t, bob, "wiki_get_page", map[string]any{"id": 5})
	if !res.IsError || !strings.Contains(textOf(res), "permission") {
		t.Fatalf("forbidden page: %+v %s", res, textOf(res))
	}
	res = call(t, bob, "wiki_get_page", map[string]any{"id": 99})
	if !res.IsError || !strings.Contains(textOf(res), "not found") {
		t.Fatalf("missing page: %s", textOf(res))
	}

	revoked := connect(t, sita.Credentials{Token: "revoked"})
	res = call(t, revoked, "wiki_search", map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(textOf(res), "API token") {
		t.Fatalf("revoked token: %s", textOf(res))
	}
}
