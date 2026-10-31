package wiki

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
)

// node is a wiki node found while crawling.
type node struct {
	Ref
	parent int // index into the crawl result, -1 for the root
	depth  int
}

// crawl walks the tree below root breadth-first, at most maxDepth levels deep
// (negative means unlimited) and at most limit nodes. Subtrees the user may not
// read are skipped. truncated reports whether limit cut the crawl short.
func (s *Service) crawl(ctx context.Context, creds sita.Credentials, root Ref, maxDepth, limit int) (nodes []node, truncated bool, err error) {
	nodes = []node{{Ref: root, parent: -1}}
	seen := map[int]bool{root.ID: true}
	frontier := []int{0}

	for depth := 0; len(frontier) > 0 && (maxDepth < 0 || depth < maxDepth); depth++ {
		children := make([][]sita.Button, len(frontier))
		errs := make([]error, len(frontier))
		parallel(len(frontier), func(i int) {
			children[i], errs[i] = s.client.Children(ctx, creds, nodes[frontier[i]].ID)
		})

		var next []int
		for i, parent := range frontier {
			if err := errs[i]; err != nil {
				if depth == 0 || ctx.Err() != nil {
					return nil, false, err
				}
				if errors.Is(err, sita.ErrForbidden) || errors.Is(err, sita.ErrNotFound) {
					continue
				}
				return nil, false, err
			}
			for _, c := range children[i] {
				if seen[c.ID] {
					continue
				}
				if len(nodes) >= limit {
					return nodes, true, nil
				}
				seen[c.ID] = true
				nodes = append(nodes, node{Ref: Ref(c), parent: parent, depth: depth + 1})
				if c.Type == sita.NodeTopic {
					next = append(next, len(nodes)-1)
				}
			}
		}
		frontier = next
	}
	return nodes, false, nil
}

// Tree is an outline of a part of the wiki.
type Tree struct {
	Outline   string `json:"outline" jsonschema:"indented list, one node per line as '[id] title (TYPE)'"`
	Nodes     int    `json:"nodes"`
	Truncated bool   `json:"truncated" jsonschema:"true if the node limit was reached and the outline is incomplete"`
}

// Tree returns an outline of the subtree below topic rootID, maxDepth levels deep.
func (s *Service) Tree(ctx context.Context, creds sita.Credentials, rootID, maxDepth int) (Tree, error) {
	topic, err := s.client.Topic(ctx, creds, rootID)
	if errors.Is(err, sita.ErrNotFound) {
		return Tree{}, fmt.Errorf("node %d is not a topic or does not exist", rootID)
	}
	if err != nil {
		return Tree{}, err
	}
	nodes, truncated, err := s.crawl(ctx, creds, Ref{ID: topic.ID, Title: topic.Topic, Type: sita.NodeTopic}, maxDepth, s.maxNodes)
	if err != nil {
		return Tree{}, err
	}

	kids := map[int][]int{}
	for i, n := range nodes {
		if n.parent >= 0 {
			kids[n.parent] = append(kids[n.parent], i)
		}
	}
	var sb strings.Builder
	var write func(i, indent int)
	write = func(i, indent int) {
		n := nodes[i]
		fmt.Fprintf(&sb, "%s- [%d] %s (%s)\n", strings.Repeat("  ", indent), n.ID, n.Title, n.Type)
		for _, k := range kids[i] {
			write(k, indent+1)
		}
	}
	write(0, 0)
	if truncated {
		sb.WriteString("… (node limit reached, use a deeper root_id to see more)\n")
	}
	return Tree{Outline: sb.String(), Nodes: len(nodes), Truncated: truncated}, nil
}

// parallel runs fn(0..n-1) with at most `workers` calls at a time.
func parallel(n int, fn func(i int)) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			fn(i)
		})
	}
	wg.Wait()
}
