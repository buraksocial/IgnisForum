// Package domain holds the forum's persisted entities. Every entity is stored as
// a single JSON blob (primary record) under its key from the key schema (§4).
// Field updates are read-modify-write; there is no per-column storage.
//
// State values follow the XenForo model: 0 = visible/valid, 1 = pending
// moderation, 2 = deleted. Keep them in sync with repo/state.go.
package domain

import "time"

// User is a forum member.
type User struct {
	ID           uint64            `json:"id"`
	Username     string            `json:"username"`
	EmailHash    string            `json:"email_h"` // sha256(lower(email)), hex
	PasswordHash string            `json:"pw"`      // argon2id encoded (auth never renders user blobs)
	Title        string            `json:"title"`   // explicit user title override
	AvatarPath   string            `json:"avatar"`
	Signature    string            `json:"signature"`
	Custom       map[string]string `json:"custom,omitempty"` // custom user fields
	State        int               `json:"state"`
	GroupIDs     []uint64          `json:"group_ids"`
	TrophyPoints int               `json:"trophy_points"`
	LikeScore    int               `json:"like_score"`
	WarnPoints   int               `json:"warn_points"`
	Timezone     string            `json:"timezone"`
	Locale       string            `json:"locale"`
	Opts         map[string]any    `json:"opts,omitempty"`
	JoinedAt     time.Time         `json:"joined_at"`
	LastSeen     time.Time         `json:"last_seen"`
	WPUserID     int               `json:"wp_user_id,omitempty"` // optional WP bridge link
}

// NodeType distinguishes categories from forums in the node tree.
type NodeType string

const (
	TypeCategory NodeType = "category"
	TypeForum    NodeType = "forum"
)

// Node is a forum node (category or forum). Its position in the parent's child
// list is kept in the idx:children ZSET; counts/last_post are denormalised here
// so the whole forum index renders from ONE blob read.
type Node struct {
	ID          uint64       `json:"id"`
	Title       string       `json:"title"`
	Slug        string       `json:"slug"`
	ParentID    uint64       `json:"parent_id"` // 0 = root
	Position    int          `json:"position"`
	Type        NodeType     `json:"type"`
	Description string       `json:"description"`
	ThreadCount int          `json:"thread_count"`
	PostCount   int          `json:"post_count"`
	LastPost    *LastPostRef `json:"last_post,omitempty"`
	Options     NodeOptions  `json:"options"`
}

// NodeOptions are per-forum toggles (prefix ids etc. land in later milestones).
type NodeOptions struct {
	Prefixes   []uint64 `json:"prefixes,omitempty"`
	AllowPolls bool     `json:"allow_polls"`
}

// ThreadStatus is the moderation/closure state of a thread.
type ThreadStatus string

const (
	ThreadOpen    ThreadStatus = "open"
	ThreadClosed  ThreadStatus = "closed"
	ThreadDeleted ThreadStatus = "deleted"
	ThreadMoved   ThreadStatus = "moved"
)

// Thread is a discussion topic.
type Thread struct {
	ID              uint64       `json:"id"`
	NodeID          uint64       `json:"node_id"`
	Title           string       `json:"title"`
	Slug            string       `json:"slug"`
	PrefixID        uint64       `json:"prefix_id,omitempty"`
	UserID          uint64       `json:"user_id"`
	FirstPostID     uint64       `json:"first_post_id"`
	PostCount       int          `json:"post_count"`
	ViewCount       int          `json:"view_count"`
	Status          ThreadStatus `json:"status"`
	MoveToThreadID  uint64       `json:"move_to_thread_id,omitempty"`
	PollID          uint64       `json:"poll_id,omitempty"`
	LikeScore       int          `json:"like_score"`
	DiscussionState int          `json:"discussion_state"` // thread-level moderation state
	LastPost        *LastPostRef `json:"last_post,omitempty"`
	Date            time.Time    `json:"date"`
	Version         uint64       `json:"version"` // optimistic concurrency marker
}

// Post is a message inside a thread.
type Post struct {
	ID              uint64         `json:"id"`
	ThreadID        uint64         `json:"thread_id"`
	UserID          uint64         `json:"user_id"`
	MessageRaw      string         `json:"message_raw"`
	MessageRendered string         `json:"message_rendered"`
	Date            time.Time      `json:"date"`
	State           int            `json:"state"` // 0 visible, 1 moderated, 2 deleted
	EditCount       int            `json:"edit_count"`
	ReactionCounts  map[string]int `json:"reaction_counts,omitempty"` // reaction code → count
	AttachmentIDs   []uint64       `json:"attachment_ids,omitempty"`
	IPHash          string         `json:"ip_h,omitempty"`
	UAHash          string         `json:"ua_h,omitempty"`
	Version         uint64         `json:"version"`
}

// LastPostRef is the denormalised "last post" card stored inside node/thread
// blobs. It is what makes the forum index a single-blob render: no extra reads
// per row for the last-post column.
type LastPostRef struct {
	T      time.Time `json:"t"`
	PostID uint64    `json:"post_id"`
	UserID uint64    `json:"user_id"`
}

// Prefix is a thread label ("Soru", "Çözüm"…).
type Prefix struct {
	ID           uint64 `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	DisplayOrder int    `json:"display_order"`
}

// Tag is a free-form thread label with its own threads index.
type Tag struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	ThreadCount int    `json:"thread_count"`
}
