package sita

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// NodeType is the kind of a wiki node.
type NodeType string

// Wiki node types as returned by the backend.
const (
	NodeTopic NodeType = "TOPIC"
	NodeEntry NodeType = "ENTRY"
	NodePoll  NodeType = "POLL"
)

// RootID is the id of the wiki start page ("Startseite").
const RootID = 0

// Topic is a wiki topic: a titled node that holds other nodes.
type Topic struct {
	ID    int    `json:"id"`
	Topic string `json:"topic"`
}

// Entry is a wiki article. Content is HTML.
type Entry struct {
	ID       int    `json:"id"`
	Headline string `json:"headline"`
	Content  string `json:"content"`
}

// Button is a node reference as shown in the wiki navigation.
type Button struct {
	ID    int      `json:"id"`
	Title string   `json:"title"`
	Type  NodeType `json:"type"`
}

// Poll is a wiki poll with the calling user's own votes.
type Poll struct {
	WikiID      *int         `json:"wikiId"`
	Question    string       `json:"question"`
	Description string       `json:"description"`
	Options     []PollOption `json:"options"`
	Date        *string      `json:"date"`
}

// PollOption is one answer of a poll. Vote is the caller's vote ("TRUE"/"FALSE").
type PollOption struct {
	ID     string `json:"id"`
	Option string `json:"option"`
	Vote   string `json:"vote"`
}

// ErrPollClosed is returned by Poll for polls that have ended; the backend
// does not return their content.
var ErrPollClosed = errors.New("poll is closed")

// NodeType returns the type of the wiki node id.
func (c *Client) NodeType(ctx context.Context, creds Credentials, id int) (NodeType, error) {
	var t string
	if err := c.getJSON(ctx, creds, fmt.Sprintf("/api/wiki/types/%d", id), &t); err != nil {
		return "", err
	}
	nt := NodeType(strings.ToUpper(t))
	switch nt {
	case NodeTopic, NodeEntry, NodePoll:
		return nt, nil
	}
	// The backend returns null for unknown ids.
	return "", ErrNotFound
}

// Topic returns the topic id.
func (c *Client) Topic(ctx context.Context, creds Credentials, id int) (Topic, error) {
	var t Topic
	err := c.getJSON(ctx, creds, fmt.Sprintf("/api/wiki/topics/%d", id), &t)
	return t, err
}

// Entry returns the entry id.
func (c *Client) Entry(ctx context.Context, creds Credentials, id int) (Entry, error) {
	var e Entry
	err := c.getJSON(ctx, creds, fmt.Sprintf("/api/wiki/entries/%d", id), &e)
	return e, err
}

// Children returns the direct children of topic id that the user may read.
func (c *Client) Children(ctx context.Context, creds Credentials, id int) ([]Button, error) {
	var b []Button
	err := c.getJSON(ctx, creds, fmt.Sprintf("/api/wiki/buttons/main-panel/%d", id), &b)
	return b, err
}

// Parent returns the topic that contains node id. The root has no parent and yields ErrNotFound.
func (c *Client) Parent(ctx context.Context, creds Credentials, id int) (Button, error) {
	// The left panel lists the parent topic first, followed by its children.
	var b []Button
	if err := c.getJSON(ctx, creds, fmt.Sprintf("/api/wiki/buttons/left-panel/%d", id), &b); err != nil {
		return Button{}, err
	}
	if len(b) == 0 {
		return Button{}, ErrNotFound
	}
	return b[0], nil
}

// PollClosed reports whether poll id has ended.
func (c *Client) PollClosed(ctx context.Context, creds Credentials, id int) (bool, error) {
	var closed bool
	err := c.getJSON(ctx, creds, fmt.Sprintf("/api/poll/closed/%d", id), &closed)
	return closed, err
}

// Poll returns poll id. Closed polls yield ErrPollClosed.
func (c *Client) Poll(ctx context.Context, creds Credentials, id int) (Poll, error) {
	closed, err := c.PollClosed(ctx, creds, id)
	if err != nil {
		return Poll{}, err
	}
	if closed {
		return Poll{}, ErrPollClosed
	}
	var p Poll
	err = c.getJSON(ctx, creds, fmt.Sprintf("/api/poll/get/%d", id), &p)
	return p, err
}
