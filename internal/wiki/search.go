package wiki

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
)

// snippetRadius is how many characters around a match a snippet shows.
const snippetRadius = 80

// index is the searchable content of the wiki as one user sees it.
type index struct {
	nodes     []indexedNode
	truncated bool
}

type indexedNode struct {
	Ref
	breadcrumb string
	text       string // entry content as Markdown
	lowerTitle string
	lowerText  string
}

type indexEntry struct {
	mu    sync.Mutex
	idx   *index
	built time.Time
}

// SearchResult is one search hit.
type SearchResult struct {
	ID         int           `json:"id"`
	Title      string        `json:"title"`
	Type       sita.NodeType `json:"type"`
	Breadcrumb string        `json:"breadcrumb" jsonschema:"path of parent topics, separated by ' › '"`
	Snippet    string        `json:"snippet,omitempty" jsonschema:"text around the first match in the content"`
	URL        string        `json:"url"`
}

// SearchResults is the result of a search.
type SearchResults struct {
	Results   []SearchResult `json:"results"`
	Partial   bool           `json:"partial" jsonschema:"true if no node matched all words and results match only some of them"`
	Truncated bool           `json:"truncated" jsonschema:"true if the wiki has more nodes than were indexed"`
}

// Search finds wiki nodes whose title or content contain the words of query.
// Nodes matching all words come first; if there are none, nodes matching any word are returned.
func (s *Service) Search(ctx context.Context, creds sita.Credentials, query string, limit int) (SearchResults, error) {
	words := strings.Fields(strings.Map(unicode.ToLower, query))
	if len(words) == 0 {
		return SearchResults{}, errors.New("query must not be empty")
	}
	idx, err := s.index(ctx, creds)
	if err != nil {
		return SearchResults{}, err
	}

	type hit struct {
		n     *indexedNode
		score int
		all   bool
	}
	var hits []hit
	anyAll := false
	for i := range idx.nodes {
		n := &idx.nodes[i]
		score, matched := 0, 0
		for _, w := range words {
			inTitle := strings.Contains(n.lowerTitle, w)
			inText := strings.Count(n.lowerText, w)
			if inTitle || inText > 0 {
				matched++
			}
			if inTitle {
				score += 10
			}
			score += min(inText, 5)
		}
		if matched == 0 {
			continue
		}
		all := matched == len(words)
		anyAll = anyAll || all
		hits = append(hits, hit{n: n, score: score, all: all})
	}
	if anyAll {
		hits = slices.DeleteFunc(hits, func(h hit) bool { return !h.all })
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(b.score, a.score) })

	res := SearchResults{Results: []SearchResult{}, Partial: len(hits) > 0 && !anyAll, Truncated: idx.truncated}
	for _, h := range hits[:min(limit, len(hits))] {
		res.Results = append(res.Results, SearchResult{
			ID:         h.n.ID,
			Title:      h.n.Title,
			Type:       h.n.Type,
			Breadcrumb: h.n.breadcrumb,
			Snippet:    snippet(h.n.text, h.n.lowerText, words),
			URL:        s.URL(h.n.ID),
		})
	}
	return res, nil
}

// index returns the user's search index, building it if it is missing or older than the TTL.
func (s *Service) index(ctx context.Context, creds sita.Credentials) (*index, error) {
	key := creds.Key()
	now := s.now()

	s.mu.Lock()
	for k, e := range s.indexes {
		if k != key && e.mu.TryLock() {
			if e.idx != nil && now.Sub(e.built) > s.ttl {
				delete(s.indexes, k)
			}
			e.mu.Unlock()
		}
	}
	e, ok := s.indexes[key]
	if !ok {
		e = &indexEntry{}
		s.indexes[key] = e
	}
	s.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idx != nil && now.Sub(e.built) <= s.ttl {
		return e.idx, nil
	}
	idx, err := s.buildIndex(ctx, creds)
	if err != nil {
		return nil, err
	}
	e.idx, e.built = idx, now
	return idx, nil
}

func (s *Service) buildIndex(ctx context.Context, creds sita.Credentials) (*index, error) {
	root, err := s.client.Topic(ctx, creds, sita.RootID)
	if err != nil {
		return nil, err
	}
	nodes, truncated, err := s.crawl(ctx, creds, Ref{ID: root.ID, Title: root.Topic, Type: sita.NodeTopic}, -1, s.maxNodes)
	if err != nil {
		return nil, err
	}

	texts := make([]string, len(nodes))
	errs := make([]error, len(nodes))
	parallel(len(nodes), func(i int) {
		if nodes[i].Type != sita.NodeEntry {
			return
		}
		entry, err := s.client.Entry(ctx, creds, nodes[i].ID)
		if err != nil {
			errs[i] = err
			return
		}
		texts[i], errs[i] = ToMarkdown(entry.Content)
	})

	idx := &index{truncated: truncated}
	for i, n := range nodes {
		if err := errs[i]; err != nil {
			if errors.Is(err, sita.ErrForbidden) || errors.Is(err, sita.ErrNotFound) {
				continue
			}
			return nil, err
		}
		var crumbs []string
		for p := n.parent; p >= 0; p = nodes[p].parent {
			crumbs = append([]string{nodes[p].Title}, crumbs...)
		}
		idx.nodes = append(idx.nodes, indexedNode{
			Ref:        n.Ref,
			breadcrumb: strings.Join(crumbs, " › "),
			text:       texts[i],
			lowerTitle: strings.Map(unicode.ToLower, n.Title),
			lowerText:  strings.Map(unicode.ToLower, texts[i]),
		})
	}
	return idx, nil
}

// snippet returns the text around the first match of any word.
// lower must be strings.Map(unicode.ToLower, text), which keeps rune positions aligned.
func snippet(text, lower string, words []string) string {
	pos := -1
	for _, w := range words {
		if i := strings.Index(lower, w); i >= 0 && (pos < 0 || i < pos) {
			pos = i
		}
	}
	if pos < 0 {
		return ""
	}
	runes := []rune(text)
	at := utf8.RuneCountInString(lower[:pos])
	start, end := max(0, at-snippetRadius), min(len(runes), at+snippetRadius)
	s := strings.Join(strings.Fields(string(runes[start:end])), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s
}
