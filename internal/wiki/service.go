// Package wiki implements the wiki features exposed as MCP tools on top of the sita-backend client.
package wiki

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
)

// workers limits concurrent backend requests per crawl.
const workers = 4

// maxBreadcrumbDepth guards against cycles when walking up the tree.
const maxBreadcrumbDepth = 32

// Service provides read access to the wiki on behalf of a user.
type Service struct {
	client    *sita.Client
	publicURL string
	ttl       time.Duration
	maxNodes  int
	now       func() time.Time

	mu      sync.Mutex
	indexes map[string]*indexEntry
}

// Options configure a Service.
type Options struct {
	// PublicURL is the prefix of public wiki links; the node id is appended.
	PublicURL string
	// IndexTTL is how long a user's search index is reused.
	IndexTTL time.Duration
	// MaxNodes caps how many nodes a crawl visits.
	MaxNodes int
}

// NewService creates a Service.
func NewService(client *sita.Client, opts Options) *Service {
	return &Service{
		client:    client,
		publicURL: opts.PublicURL,
		ttl:       opts.IndexTTL,
		maxNodes:  opts.MaxNodes,
		now:       time.Now,
		indexes:   map[string]*indexEntry{},
	}
}

// Ref references a wiki node.
type Ref struct {
	ID    int           `json:"id"`
	Title string        `json:"title"`
	Type  sita.NodeType `json:"type"`
}

// Page is a wiki node with its content.
type Page struct {
	ID         int           `json:"id"`
	Type       sita.NodeType `json:"type"`
	Title      string        `json:"title"`
	URL        string        `json:"url"`
	Breadcrumb []Ref         `json:"breadcrumb" jsonschema:"path from the start page down to the parent of this node"`
	Children   []Ref         `json:"children,omitempty" jsonschema:"direct children, only for topics"`
	Content    string        `json:"content,omitempty" jsonschema:"article text as Markdown, only for entries"`
	Poll       *PollInfo     `json:"poll,omitempty" jsonschema:"poll details, only for polls"`
}

// PollInfo describes a poll.
type PollInfo struct {
	Closed      bool         `json:"closed"`
	Description string       `json:"description,omitempty"`
	EndDate     string       `json:"end_date,omitempty"`
	Options     []PollOption `json:"options,omitempty" jsonschema:"answer options; empty for closed polls, whose content the backend does not return"`
}

// PollOption is one answer of a poll.
type PollOption struct {
	Text       string `json:"text"`
	OwnVoteYes bool   `json:"own_vote_yes" jsonschema:"true if the calling user voted yes for this option"`
}

// URL returns the public link of node id.
func (s *Service) URL(id int) string {
	return s.publicURL + strconv.Itoa(id)
}

// Page loads node id with its content, children and breadcrumb.
func (s *Service) Page(ctx context.Context, creds sita.Credentials, id int) (Page, error) {
	typ, err := s.client.NodeType(ctx, creds, id)
	if err != nil {
		return Page{}, err
	}
	page := Page{ID: id, Type: typ, URL: s.URL(id)}

	if page.Breadcrumb, err = s.Breadcrumb(ctx, creds, id); err != nil {
		return Page{}, err
	}

	switch typ {
	case sita.NodeTopic:
		topic, err := s.client.Topic(ctx, creds, id)
		if err != nil {
			return Page{}, err
		}
		page.Title = topic.Topic
		children, err := s.client.Children(ctx, creds, id)
		if err != nil {
			return Page{}, err
		}
		page.Children = refs(children)

	case sita.NodeEntry:
		entry, err := s.client.Entry(ctx, creds, id)
		if err != nil {
			return Page{}, err
		}
		page.Title = entry.Headline
		if page.Content, err = ToMarkdown(entry.Content); err != nil {
			return Page{}, err
		}

	case sita.NodePoll:
		poll, err := s.client.Poll(ctx, creds, id)
		switch {
		case errors.Is(err, sita.ErrPollClosed):
			page.Poll = &PollInfo{Closed: true}
			// Closed polls have no content endpoint, so take the title from the parent's listing.
			page.Title = s.titleFromParent(ctx, creds, page.Breadcrumb, id)
		case err != nil:
			return Page{}, err
		default:
			page.Title = poll.Question
			page.Poll = &PollInfo{Description: poll.Description}
			if poll.Date != nil {
				page.Poll.EndDate = *poll.Date
			}
			for _, o := range poll.Options {
				page.Poll.Options = append(page.Poll.Options, PollOption{Text: o.Option, OwnVoteYes: o.Vote == "TRUE"})
			}
		}
	}
	return page, nil
}

func (s *Service) titleFromParent(ctx context.Context, creds sita.Credentials, breadcrumb []Ref, id int) string {
	if len(breadcrumb) == 0 {
		return ""
	}
	siblings, err := s.client.Children(ctx, creds, breadcrumb[len(breadcrumb)-1].ID)
	if err != nil {
		return ""
	}
	for _, b := range siblings {
		if b.ID == id {
			return b.Title
		}
	}
	return ""
}

// Breadcrumb returns the topics from the start page down to the parent of id.
// Ancestors the user may not read end the path early.
func (s *Service) Breadcrumb(ctx context.Context, creds sita.Credentials, id int) ([]Ref, error) {
	var path []Ref
	cur := id
	for range maxBreadcrumbDepth {
		if cur == sita.RootID {
			break
		}
		parent, err := s.client.Parent(ctx, creds, cur)
		if errors.Is(err, sita.ErrNotFound) || errors.Is(err, sita.ErrForbidden) {
			break
		}
		if err != nil {
			return nil, err
		}
		if parent.ID == cur {
			break
		}
		path = append([]Ref{{ID: parent.ID, Title: parent.Title, Type: sita.NodeTopic}}, path...)
		cur = parent.ID
	}
	return path, nil
}

// Children lists the direct children of topic id.
func (s *Service) Children(ctx context.Context, creds sita.Credentials, id int) ([]Ref, error) {
	typ, err := s.client.NodeType(ctx, creds, id)
	if err != nil {
		return nil, err
	}
	if typ != sita.NodeTopic {
		return nil, fmt.Errorf("node %d is a %s, only topics have children", id, typ)
	}
	children, err := s.client.Children(ctx, creds, id)
	if err != nil {
		return nil, err
	}
	return refs(children), nil
}

func refs(buttons []sita.Button) []Ref {
	res := make([]Ref, 0, len(buttons))
	for _, b := range buttons {
		res = append(res, Ref(b))
	}
	return res
}

// ToMarkdown converts the HTML body of an entry to Markdown.
func ToMarkdown(html string) (string, error) {
	md, err := htmltomarkdown.ConvertString(html)
	if err != nil {
		return "", fmt.Errorf("convert entry HTML to Markdown: %w", err)
	}
	return md, nil
}
