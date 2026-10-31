// Package mcpserver registers the wiki tools on an MCP server.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
	"github.com/SimpletacticsGaming/sita-mcp/internal/wiki"
)

const (
	defaultTreeDepth = 3
	maxTreeDepth     = 6
	defaultLimit     = 10
	maxLimit         = 50
)

// CredentialsFunc returns the sita credentials to use for a tool call.
type CredentialsFunc func(req *mcp.CallToolRequest) (sita.Credentials, error)

const instructions = `Read access to the wiki of Simpletactics Gaming (simpletactics.de), a German gaming community.
The wiki is a tree: topics (TOPIC) contain entries (ENTRY, articles), polls (POLL) and further topics.
The start page "Startseite" has id 0. Content is mostly German, so search with German words.
Start with wiki_search to find pages, or wiki_get_tree to get an overview, then read pages with wiki_get_page.
Only pages the signed-in user may read are visible.`

// New creates the MCP server with all wiki tools.
func New(svc *wiki.Service, creds CredentialsFunc, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "sita-mcp", Title: "Simpletactics Wiki", Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	t := tools{svc: svc, creds: creds}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_search",
		Title:       "Search the wiki",
		Description: "Full-text search over titles and article content of the Simpletactics wiki. Words are matched case-insensitively as substrings; pages containing all words rank first. Content is German.",
		Annotations: readOnly("Search the wiki"),
	}, t.search)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_get_page",
		Title:       "Read a wiki page",
		Description: "Read one wiki node by id: articles (ENTRY) as Markdown, topics (TOPIC) with their children, polls (POLL) with options and the user's own votes. Includes the breadcrumb and the public URL.",
		Annotations: readOnly("Read a wiki page"),
	}, t.getPage)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_list_children",
		Title:       "List topic children",
		Description: "List the direct children (topics, entries, polls) of a wiki topic. The start page has id 0.",
		Annotations: readOnly("List topic children"),
	}, t.listChildren)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_get_tree",
		Title:       "Wiki outline",
		Description: fmt.Sprintf("Get an indented outline of the wiki below a topic, showing ids, titles and types. Default depth %d, max %d.", defaultTreeDepth, maxTreeDepth),
		Annotations: readOnly("Wiki outline"),
	}, t.getTree)

	return s
}

func readOnly(title string) *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}
}

type tools struct {
	svc   *wiki.Service
	creds CredentialsFunc
}

type searchIn struct {
	Query string `json:"query" jsonschema:"words to search for, e.g. 'Initiative Kampf'"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results, default 10, max 50"`
}

func (t tools) search(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, wiki.SearchResults, error) {
	creds, err := t.creds(req)
	if err != nil {
		return nil, wiki.SearchResults{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	res, err := t.svc.Search(ctx, creds, in.Query, min(limit, maxLimit))
	if err != nil {
		return nil, wiki.SearchResults{}, toolError(err)
	}

	var sb strings.Builder
	switch {
	case len(res.Results) == 0:
		fmt.Fprintf(&sb, "No wiki pages found for %q.\n", in.Query)
	case res.Partial:
		fmt.Fprintf(&sb, "No page contains all words of %q; these contain some of them:\n\n", in.Query)
	default:
		fmt.Fprintf(&sb, "%d result(s) for %q:\n\n", len(res.Results), in.Query)
	}
	for _, r := range res.Results {
		fmt.Fprintf(&sb, "- [%d] %s (%s) — %s\n", r.ID, r.Title, r.Type, r.Breadcrumb)
		if r.Snippet != "" {
			fmt.Fprintf(&sb, "  %s\n", r.Snippet)
		}
	}
	if res.Truncated {
		sb.WriteString("\nNote: the wiki is larger than the search index limit, some pages were not searched.\n")
	}
	return text(sb.String()), res, nil
}

type pageIn struct {
	ID int `json:"id" jsonschema:"id of the wiki node; the start page is 0"`
}

func (t tools) getPage(ctx context.Context, req *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, wiki.Page, error) {
	creds, err := t.creds(req)
	if err != nil {
		return nil, wiki.Page{}, err
	}
	page, err := t.svc.Page(ctx, creds, in.ID)
	if err != nil {
		return nil, wiki.Page{}, toolError(err)
	}
	return text(renderPage(page)), page, nil
}

type childrenIn struct {
	TopicID int `json:"topic_id,omitempty" jsonschema:"id of the topic, default 0 (start page)"`
}

type childrenOut struct {
	Children []wiki.Ref `json:"children"`
}

func (t tools) listChildren(ctx context.Context, req *mcp.CallToolRequest, in childrenIn) (*mcp.CallToolResult, childrenOut, error) {
	creds, err := t.creds(req)
	if err != nil {
		return nil, childrenOut{}, err
	}
	children, err := t.svc.Children(ctx, creds, in.TopicID)
	if err != nil {
		return nil, childrenOut{}, toolError(err)
	}
	var sb strings.Builder
	if len(children) == 0 {
		fmt.Fprintf(&sb, "Topic %d has no visible children.\n", in.TopicID)
	}
	for _, c := range children {
		fmt.Fprintf(&sb, "- [%d] %s (%s)\n", c.ID, c.Title, c.Type)
	}
	return text(sb.String()), childrenOut{Children: children}, nil
}

type treeIn struct {
	RootID   int `json:"root_id,omitempty" jsonschema:"id of the topic to start at, default 0 (start page)"`
	MaxDepth int `json:"max_depth,omitempty" jsonschema:"how many levels below the root to show, default 3, max 6"`
}

func (t tools) getTree(ctx context.Context, req *mcp.CallToolRequest, in treeIn) (*mcp.CallToolResult, wiki.Tree, error) {
	creds, err := t.creds(req)
	if err != nil {
		return nil, wiki.Tree{}, err
	}
	depth := in.MaxDepth
	if depth <= 0 {
		depth = defaultTreeDepth
	}
	tree, err := t.svc.Tree(ctx, creds, in.RootID, min(depth, maxTreeDepth))
	if err != nil {
		return nil, wiki.Tree{}, toolError(err)
	}
	return text(tree.Outline), tree, nil
}

func renderPage(p wiki.Page) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", p.Title)
	crumbs := make([]string, 0, len(p.Breadcrumb))
	for _, c := range p.Breadcrumb {
		crumbs = append(crumbs, fmt.Sprintf("%s [%d]", c.Title, c.ID))
	}
	fmt.Fprintf(&sb, "id: %d · type: %s · url: %s\n", p.ID, p.Type, p.URL)
	if len(crumbs) > 0 {
		fmt.Fprintf(&sb, "path: %s\n", strings.Join(crumbs, " › "))
	}
	sb.WriteString("\n")

	switch {
	case p.Type == sita.NodeEntry:
		sb.WriteString(p.Content)
		sb.WriteString("\n")
	case p.Type == sita.NodeTopic:
		if len(p.Children) == 0 {
			sb.WriteString("This topic has no visible children.\n")
		}
		for _, c := range p.Children {
			fmt.Fprintf(&sb, "- [%d] %s (%s)\n", c.ID, c.Title, c.Type)
		}
	case p.Poll != nil && p.Poll.Closed:
		sb.WriteString("This poll is closed; its options and votes are not available.\n")
	case p.Poll != nil:
		if p.Poll.Description != "" {
			fmt.Fprintf(&sb, "%s\n\n", p.Poll.Description)
		}
		if p.Poll.EndDate != "" {
			fmt.Fprintf(&sb, "Open until: %s\n\n", p.Poll.EndDate)
		}
		sb.WriteString("Options (your vote):\n")
		for _, o := range p.Poll.Options {
			vote := "no"
			if o.OwnVoteYes {
				vote = "yes"
			}
			fmt.Fprintf(&sb, "- %s (%s)\n", o.Text, vote)
		}
	}
	return sb.String()
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// toolError turns backend errors into messages that help the model recover.
func toolError(err error) error {
	switch {
	case errors.Is(err, sita.ErrForbidden):
		return errors.New("you do not have permission to read this wiki node")
	case errors.Is(err, sita.ErrNotFound):
		return errors.New("wiki node not found; use wiki_search or wiki_get_tree to find valid ids")
	case errors.Is(err, sita.ErrLoginFailed), errors.Is(err, sita.ErrUnauthorized):
		return fmt.Errorf("%w; check the sita credentials configured for this MCP server", err)
	}
	return err
}
