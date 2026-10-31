// Package sitatest provides an in-memory fake of the sita-backend wiki API for tests.
package sitatest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
)

// Node is a wiki node of the fake backend.
type Node struct {
	ID          int
	Type        sita.NodeType
	Title       string
	Content     string // entry HTML
	Children    []int
	Description string
	Options     []sita.PollOption
	EndDate     string
	Closed      bool
}

// Tokens of the seeded users.
const (
	AliceToken = "sita_mcp_alice_token"
	BobToken   = "sita_mcp_bob_token"
)

type session struct {
	user  string
	token string
}

// Backend is a fake sita-backend. Its fields may be changed by tests before requests are made.
type Backend struct {
	*httptest.Server

	mu       sync.Mutex
	Nodes    map[int]*Node
	Tokens   map[string]string       // MCP token → username
	Hidden   map[string]map[int]bool // username → node ids the user may not read
	sessions map[string]session      // session id → owner
	logins   int
	requests int
	nextID   int
}

// New starts a fake backend seeded with a small wiki:
//
//	0 Startseite
//	├── 1 Regeln
//	│   ├── 3 Kampfregeln (entry)
//	│   └── 4 Nächster Spieleabend? (poll)
//	└── 2 Kampagnen
//	    ├── 5 Geheime Kampagne (entry, hidden for bob)
//	    └── 6 Alte Umfrage (closed poll)
//
// Users: alice (AliceToken) may read everything, bob (BobToken) may not read nodes 2 and 5.
func New(t testing.TB) *Backend {
	b := &Backend{
		Nodes: map[int]*Node{
			0: {ID: 0, Type: sita.NodeTopic, Title: "Startseite", Children: []int{1, 2}},
			1: {ID: 1, Type: sita.NodeTopic, Title: "Regeln", Children: []int{3, 4}},
			2: {ID: 2, Type: sita.NodeTopic, Title: "Kampagnen", Children: []int{5, 6}},
			3: {ID: 3, Type: sita.NodeEntry, Title: "Kampfregeln",
				Content: "<h2>Initiative</h2><p>Initiative wird mit einem <strong>W20</strong> gewürfelt.</p><ul><li>Gleichstand: höhere Geschicklichkeit</li></ul>"},
			4: {ID: 4, Type: sita.NodePoll, Title: "Nächster Spieleabend?", Description: "Bitte bis Freitag abstimmen",
				EndDate: "2026-11-15", Options: []sita.PollOption{
					{ID: "a", Option: "Samstag", Vote: "TRUE"},
					{ID: "b", Option: "Sonntag", Vote: "FALSE"},
				}},
			5: {ID: 5, Type: sita.NodeEntry, Title: "Geheime Kampagne", Content: "<p>Der Drache lebt im Norden.</p>"},
			6: {ID: 6, Type: sita.NodePoll, Title: "Alte Umfrage", Closed: true},
		},
		Tokens:   map[string]string{AliceToken: "alice", BobToken: "bob"},
		Hidden:   map[string]map[int]bool{"bob": {2: true, 5: true}},
		sessions: map[string]session{},
	}
	b.Server = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.Close)
	return b
}

// Logins returns how many successful logins happened.
func (b *Backend) Logins() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.logins
}

// Requests returns how many requests were served.
func (b *Backend) Requests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests
}

// ExpireSessions invalidates all sessions, as if they timed out.
func (b *Backend) ExpireSessions() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions = map[string]session{}
}

// Revoke deletes token and, like ON DELETE CASCADE, every session created with it.
func (b *Backend) Revoke(token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.Tokens, token)
	for id, s := range b.sessions {
		if s.token == token {
			delete(b.sessions, id)
		}
	}
}

func (b *Backend) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests++

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/login/mcp":
		var req struct{ Token string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		user, ok := b.Tokens[req.Token]
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		b.logins++
		b.nextID++
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", b.nextID)
		b.sessions[id] = session{user: user, token: req.Token}
		writeJSON(w, map[string]any{"successful": true, "sessionId": id, "setPassword": false})
		return
	case r.Method == http.MethodGet && r.URL.Path == "/loggedIn":
		_, ok := b.sessions[r.Header.Get("authentication")]
		writeJSON(w, ok)
		return
	}

	s, ok := b.sessions[r.Header.Get("authentication")]
	if !ok {
		// Like Spring Security without an entry point: anonymous requests get 403.
		w.WriteHeader(http.StatusForbidden)
		return
	}
	user := s.user

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	route := strings.Join(parts[:len(parts)-1], "/")
	node, exists := b.Nodes[id]
	if b.Hidden[user][id] {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !exists {
		if route == "api/wiki/types" {
			writeJSON(w, nil)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch route {
	case "api/wiki/types":
		writeJSON(w, node.Type)
	case "api/wiki/topics":
		if node.Type != sita.NodeTopic {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, sita.Topic{ID: node.ID, Topic: node.Title})
	case "api/wiki/entries":
		if node.Type != sita.NodeEntry {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, sita.Entry{ID: node.ID, Headline: node.Title, Content: node.Content})
	case "api/wiki/buttons/main-panel":
		if node.Type != sita.NodeTopic {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, b.buttons(user, node.Children))
	case "api/wiki/buttons/left-panel":
		parent := b.parentOf(id)
		if parent == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, append([]sita.Button{{ID: parent.ID, Title: parent.Title, Type: sita.NodeTopic}}, b.buttons(user, parent.Children)...))
	case "api/poll/closed":
		writeJSON(w, node.Closed)
	case "api/poll/get":
		if node.Type != sita.NodePoll {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if node.Closed {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, sita.Poll{WikiID: &node.ID, Question: node.Title, Description: node.Description, Options: node.Options, Date: &node.EndDate})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (b *Backend) buttons(user string, ids []int) []sita.Button {
	res := []sita.Button{}
	for _, c := range ids {
		if n, ok := b.Nodes[c]; ok && !b.Hidden[user][c] {
			res = append(res, sita.Button{ID: n.ID, Title: n.Title, Type: n.Type})
		}
	}
	return res
}

func (b *Backend) parentOf(id int) *Node {
	for _, n := range b.Nodes {
		if slices.Contains(n.Children, id) {
			return n
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
