package repo

import (
	"fmt"
	"time"

	"github.com/buraksocial/IgnisForum/internal/bbcode"
	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// ThreadRepo owns thread creation, listing and the denormalised counters that
// make the forum index a single-blob render.
type ThreadRepo struct {
	r *Repos
}

// Create starts a thread with its first post. Write order: both primaries, then
// indexes, then node counters, then FT (§3.6).
func (t *ThreadRepo) Create(nodeID, userID uint64, title, rawMessage string) (*domain.Thread, *domain.Post, error) {
	s := t.r.S
	if _, ok := t.r.Nodes.Get(nodeID); !ok {
		return nil, nil, fmt.Errorf("node %d not found", nodeID)
	}
	now := time.Now().UTC()

	threadID := s.NextID()
	postID := s.NextID()
	s.MarkID(store.TypeThread, threadID)
	s.MarkID(store.TypePost, postID)

	thread := &domain.Thread{
		ID: threadID, NodeID: nodeID, Title: title,
		Slug:        slugFor(title, threadID),
		UserID:      userID,
		FirstPostID: postID,
		PostCount:   1,
		Status:      domain.ThreadOpen,
		LastPost:    &domain.LastPostRef{T: now, PostID: postID, UserID: userID},
		Date:        now,
	}
	post := &domain.Post{
		ID: postID, ThreadID: threadID, UserID: userID,
		MessageRaw: rawMessage, MessageRendered: bbcode.Render(rawMessage),
		Date: now, State: 0,
	}

	// 1. PRIMARIES FIRST.
	if err := s.PutJSON(keyThread(threadID), thread); err != nil {
		return nil, nil, fmt.Errorf("put thread: %w", err)
	}
	if err := s.PutJSON(keyPost(postID), post); err != nil {
		return nil, nil, fmt.Errorf("put post: %w", err)
	}

	// 2. INDEXES.
	t.indexThread(thread)
	t.r.Posts.indexPost(post)

	// 3. NODE COUNTERS (read-modify-write of the node blob): +1 post, +1 thread.
	t.r.Posts.bumpNode(nodeID, now, postID, userID, true)

	// 4. FT.
	s.FTAdd(fmt.Sprintf("post:%d", postID), title+" "+post.MessageRendered)

	return thread, post, nil
}

// Import writes a thread and all its source-mapped posts by their supplied IDs.
// It creates only primary records; the normal rebuild pass derives every index
// and full-text document after all entities have been imported. Replaying the
// same immutable source rows overwrites the same primaries rather than cloning
// discussions or replies.
func (t *ThreadRepo) Import(thread *domain.Thread, posts []*domain.Post) error {
	if thread == nil || thread.ID == 0 || thread.FirstPostID == 0 || thread.Title == "" {
		return fmt.Errorf("invalid imported thread")
	}
	if _, ok := t.r.Nodes.Get(thread.NodeID); !ok {
		return fmt.Errorf("imported thread %d has no node %d", thread.ID, thread.NodeID)
	}
	firstFound := false
	for _, post := range posts {
		if post == nil || post.ID == 0 || post.ThreadID != thread.ID {
			return fmt.Errorf("invalid post in imported thread %d", thread.ID)
		}
		if post.ID == thread.FirstPostID {
			firstFound = true
		}
	}
	if !firstFound {
		return fmt.Errorf("imported thread %d is missing first post %d", thread.ID, thread.FirstPostID)
	}
	t.r.S.MarkID(store.TypeThread, thread.ID)
	t.r.S.MarkID(store.TypePost, thread.FirstPostID)
	if err := t.r.S.PutJSON(keyThread(thread.ID), thread); err != nil {
		return err
	}
	for _, post := range posts {
		t.r.S.MarkID(store.TypePost, post.ID)
		if err := t.r.S.PutJSON(keyPost(post.ID), post); err != nil {
			return err
		}
	}
	return nil
}

// indexThread adds/updates a thread in every per-node and global thread index.
func (t *ThreadRepo) indexThread(thread *domain.Thread) {
	s := t.r.S
	lp := thread.LastPost
	if lp == nil {
		lp = &domain.LastPostRef{T: thread.Date}
	}
	s.ZAddIdx(NodeThreadsKey(thread.NodeID), ts(lp.T), thread.ID)
	s.ZAddIdx(nodeThreadsNewIdx(thread.NodeID), ts(thread.Date), thread.ID)
	s.ZAddIdx(nodeThreadsPostsIdx(thread.NodeID), float64(thread.PostCount), thread.ID)
	s.ZAddIdx(nodeThreadsVotedIdx(thread.NodeID), float64(thread.LikeScore), thread.ID)
	s.ZAddIdx(nodeThreadsViewsIdx(thread.NodeID), float64(thread.ViewCount), thread.ID)
	s.ZAddIdx(IdxLatestThreads, ts(lp.T), thread.ID)
	s.ZAddIdx(userThreadsIdx(thread.UserID), ts(thread.Date), thread.ID)
	for _, tagID := range thread.TagIDs {
		s.ZAddIdx(tagThreadsIdx(tagID), ts(thread.Date), thread.ID)
	}
}

// BumpThread updates thread counters after a new reply (called by PostRepo).
func (t *ThreadRepo) BumpThread(threadID uint64, postID, userID uint64, when time.Time) error {
	s := t.r.S
	var thread domain.Thread
	if _, err := s.GetJSON(keyThread(threadID), &thread); err != nil {
		return err
	}
	thread.PostCount++
	thread.LastPost = &domain.LastPostRef{T: when, PostID: postID, UserID: userID}
	thread.Version++
	if err := s.PutJSON(keyThread(threadID), &thread); err != nil {
		return err
	}
	t.indexThread(&thread)
	return nil
}

// IncrementViews bumps the view counter. The views index is updated in place;
// at per-node cardinality (thousands) the O(n) shift is tens of µs.
func (t *ThreadRepo) IncrementViews(threadID uint64) error {
	s := t.r.S
	var thread domain.Thread
	if _, err := s.GetJSON(keyThread(threadID), &thread); err != nil {
		return err
	}
	thread.ViewCount++
	thread.Version++
	if err := s.PutJSON(keyThread(threadID), &thread); err != nil {
		return err
	}
	s.ZAddIdx(nodeThreadsViewsIdx(thread.NodeID), float64(thread.ViewCount), thread.ID)
	return nil
}

// List returns a page of thread IDs for a node in the requested order.
// sort: "last" (default), "new", "posts", "views".
func (t *ThreadRepo) List(nodeID uint64, sort string, offset, n int) []uint64 {
	var key string
	switch sort {
	case "", "last":
		key = NodeThreadsKey(nodeID)
	case "new":
		key = nodeThreadsNewIdx(nodeID)
	case "posts":
		key = nodeThreadsPostsIdx(nodeID)
	case "views":
		key = nodeThreadsViewsIdx(nodeID)
	default:
		return nil
	}
	return t.r.S.ZPage(key, offset, n)
}

// Get returns a thread by id.
func (t *ThreadRepo) Get(id uint64) (*domain.Thread, bool) {
	var th domain.Thread
	ok, _ := t.r.S.GetJSON(keyThread(id), &th)
	if !ok {
		return nil, false
	}
	return &th, true
}

// rebuildHook re-derives a thread's index entries from its blob.
func (t *ThreadRepo) rebuildHook(key string, blob []byte) {
	var thread domain.Thread
	if err := t.r.S.DecodeJSON(blob, &thread); err != nil {
		return
	}
	t.r.Polls.rebuildCounts(&thread)
	if thread.Status == domain.ThreadDeleted {
		return
	}
	t.indexThread(&thread)
}

// slugFor builds a URL-safe slug from a title.
func slugFor(title string, id uint64) string {
	slug := bbcode.Slug(title)
	if slug == "" {
		slug = "konu"
	}
	return fmt.Sprintf("%s-%d", slug, id)
}
