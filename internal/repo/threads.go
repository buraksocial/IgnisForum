package repo

import (
	"fmt"
	"strings"
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
	return t.CreateWithApproval(nodeID, userID, title, rawMessage, true)
}

func (t *ThreadRepo) CreateWithApproval(nodeID, userID uint64, title, rawMessage string, approved bool) (*domain.Thread, *domain.Post, error) {
	s := t.r.S
	node, ok := t.r.Nodes.Get(nodeID)
	if !ok || node.Type != domain.TypeForum {
		return nil, nil, fmt.Errorf("node %d not found", nodeID)
	}
	now := time.Now().UTC()

	threadID := s.NextID()
	postID := s.NextID()
	s.MarkID(store.TypeThread, threadID)
	s.MarkID(store.TypePost, postID)

	thread := &domain.Thread{
		ID: threadID, NodeID: nodeID, Title: title,
		Slug:            slugFor(title, threadID),
		UserID:          userID,
		FirstPostID:     postID,
		PostCount:       1,
		Status:          domain.ThreadOpen,
		DiscussionState: domain.DiscussionVisible,
		LastPost:        &domain.LastPostRef{T: now, PostID: postID, UserID: userID},
		Date:            now,
	}
	post := &domain.Post{
		ID: postID, ThreadID: threadID, UserID: userID,
		MessageRaw: rawMessage, MessageRendered: bbcode.Render(rawMessage),
		Date: now, State: domain.PostVisible,
	}
	if !approved {
		thread.DiscussionState = domain.DiscussionPending
		thread.PostCount = 0
		thread.LastPost = nil
		post.State = domain.PostPending
	}

	// 1. PRIMARIES FIRST.
	if err := s.PutJSON(keyThread(threadID), thread); err != nil {
		return nil, nil, fmt.Errorf("put thread: %w", err)
	}
	if err := s.PutJSON(keyPost(postID), post); err != nil {
		return nil, nil, fmt.Errorf("put post: %w", err)
	}

	if post.State == domain.PostPending {
		s.ZAddIdx(IdxPendingPosts, ts(now), post.ID)
		s.ZAddIdx(ThreadPostsKey(thread.ID), ts(now), post.ID)
		return thread, post, nil
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
	if thread.Status == domain.ThreadDeleted || thread.DiscussionState != domain.DiscussionVisible {
		return
	}
	lp := thread.LastPost
	if lp == nil {
		lp = &domain.LastPostRef{T: thread.Date}
	}
	s.ZAddIdx(NodeThreadsKey(thread.NodeID), ts(lp.T), thread.ID)
	s.ZAddIdx(nodeThreadsNewIdx(thread.NodeID), ts(thread.Date), thread.ID)
	s.ZAddIdx(nodeThreadsPostsIdx(thread.NodeID), float64(thread.PostCount), thread.ID)
	s.ZAddIdx(nodeThreadsVotedIdx(thread.NodeID), float64(thread.LikeScore), thread.ID)
	s.ZAddIdx(nodeThreadsViewsIdx(thread.NodeID), float64(thread.ViewCount), thread.ID)
	for _, sortKey := range []string{"last", "new", "posts", "views"} {
		key := nodeThreadsRankIdx(thread.NodeID, sortKey, thread.Sticky)
		score := ts(lp.T)
		switch sortKey {
		case "new":
			score = ts(thread.Date)
		case "posts":
			score = float64(thread.PostCount)
		case "views":
			score = float64(thread.ViewCount)
		}
		s.ZAddIdx(key, score, thread.ID)
	}
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
	if thread.LastPost == nil || when.After(thread.LastPost.T) {
		thread.LastPost = &domain.LastPostRef{T: when, PostID: postID, UserID: userID}
	}
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
	if thread.Status != domain.ThreadDeleted && thread.DiscussionState == domain.DiscussionVisible {
		s.ZAddIdx(nodeThreadsViewsIdx(thread.NodeID), float64(thread.ViewCount), thread.ID)
	}
	return nil
}

// List returns a page of thread IDs for a node in the requested order.
// sort: "last" (default), "new", "posts", "views".
func (t *ThreadRepo) List(nodeID uint64, sort string, offset, n int) []uint64 {
	switch sort {
	case "", "last", "new", "posts", "views":
	default:
		return nil
	}
	if n <= 0 {
		return nil
	}
	stickyKey := nodeThreadsRankIdx(nodeID, sortOrLast(sort), true)
	normalKey := nodeThreadsRankIdx(nodeID, sortOrLast(sort), false)
	stickyCount := t.r.S.ZCard(stickyKey)
	if offset < stickyCount {
		out := t.r.S.ZPage(stickyKey, offset, n)
		if len(out) < n {
			out = append(out, t.r.S.ZPage(normalKey, 0, n-len(out))...)
		}
		return out
	}
	return t.r.S.ZPage(normalKey, offset-stickyCount, n)
}

func sortOrLast(sortKey string) string {
	if sortKey == "" {
		return "last"
	}
	return sortKey
}

// SetDiscussionState hides/restores a discussion without conflating it with
// deletion. Hidden threads are omitted from every public thread index.
func (t *ThreadRepo) SetDiscussionState(id uint64, state int) error {
	thread, ok := t.Get(id)
	if !ok || thread.Status == domain.ThreadDeleted {
		return fmt.Errorf("thread %d not found", id)
	}
	if state != domain.DiscussionVisible && state != domain.DiscussionHidden {
		return fmt.Errorf("invalid discussion state")
	}
	if thread.DiscussionState == state {
		return nil
	}
	previousState := thread.DiscussionState
	if state == domain.DiscussionVisible {
		if first, found := t.r.Posts.Get(thread.FirstPostID); found && first.State == domain.PostPending {
			state = domain.DiscussionPending
		}
	}
	thread.DiscussionState = state
	thread.Version++
	if err := t.r.S.PutJSON(keyThread(id), thread); err != nil {
		return err
	}
	t.removeThreadIndexes(thread)
	t.r.Posts.RefreshThreadVisibility(thread)
	t.indexThread(thread)
	if (previousState == domain.DiscussionVisible) != (state == domain.DiscussionVisible) {
		if err := t.adjustNodeVisibility(thread, state == domain.DiscussionVisible); err != nil {
			return err
		}
	}
	return nil
}

// adjustNodeVisibility keeps public forum counters and last-post cards in sync
// when a discussion enters or leaves the public indexes.
func (t *ThreadRepo) adjustNodeVisibility(thread *domain.Thread, visible bool) error {
	node, ok := t.r.Nodes.Get(thread.NodeID)
	if !ok {
		return fmt.Errorf("node %d not found", thread.NodeID)
	}
	if visible {
		node.ThreadCount++
		node.PostCount += thread.PostCount
		if thread.LastPost != nil && (node.LastPost == nil || thread.LastPost.T.After(node.LastPost.T)) {
			node.LastPost = thread.LastPost
		}
	} else {
		node.ThreadCount--
		node.PostCount -= thread.PostCount
		if node.ThreadCount < 0 {
			node.ThreadCount = 0
		}
		if node.PostCount < 0 {
			node.PostCount = 0
		}
		if node.LastPost != nil && thread.LastPost != nil && node.LastPost.PostID == thread.LastPost.PostID {
			node.LastPost = t.r.Posts.latestVisibleInNode(node.ID)
		}
	}
	return t.r.S.PutJSON(keyNode(node.ID), node)
}

func (t *ThreadRepo) SetLocked(id uint64, locked bool) error {
	thread, ok := t.Get(id)
	if !ok || thread.Status == domain.ThreadDeleted {
		return fmt.Errorf("thread %d not found", id)
	}
	if locked {
		thread.Status = domain.ThreadClosed
	} else {
		thread.Status = domain.ThreadOpen
	}
	thread.Version++
	t.removeThreadIndexes(thread)
	if err := t.r.S.PutJSON(keyThread(id), thread); err != nil {
		return err
	}
	t.indexThread(thread)
	return nil
}

func (t *ThreadRepo) Rename(id uint64, title string) error {
	thread, ok := t.Get(id)
	if !ok || thread.Status == domain.ThreadDeleted {
		return fmt.Errorf("thread %d not found", id)
	}
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 160 {
		return fmt.Errorf("invalid thread title")
	}
	thread.Title = title
	thread.Slug = slugFor(title, id)
	thread.Version++
	if err := t.r.S.PutJSON(keyThread(id), thread); err != nil {
		return err
	}
	for _, postID := range t.r.Posts.List(id, 0, t.r.S.ZCard(ThreadPostsKey(id))) {
		post, ok := t.r.Posts.Get(postID)
		if !ok || post.State != domain.PostVisible {
			continue
		}
		if thread.DiscussionState == domain.DiscussionVisible {
			t.r.S.FTAdd(fmt.Sprintf("post:%d", post.ID), title+" "+post.MessageRendered)
		} else {
			t.r.S.FTRemove(fmt.Sprintf("post:%d", post.ID))
		}
	}
	return nil
}

func (t *ThreadRepo) SetSticky(id uint64, sticky bool) error {
	thread, ok := t.Get(id)
	if !ok || thread.Status == domain.ThreadDeleted {
		return fmt.Errorf("thread %d not found", id)
	}
	t.removeThreadIndexes(thread)
	thread.Sticky = sticky
	thread.Version++
	if err := t.r.S.PutJSON(keyThread(id), thread); err != nil {
		return err
	}
	t.indexThread(thread)
	return nil
}

func (t *ThreadRepo) removeThreadIndexes(thread *domain.Thread) {
	s := t.r.S
	s.ZRem(NodeThreadsKey(thread.NodeID), thread.ID)
	s.ZRem(nodeThreadsNewIdx(thread.NodeID), thread.ID)
	s.ZRem(nodeThreadsPostsIdx(thread.NodeID), thread.ID)
	s.ZRem(nodeThreadsVotedIdx(thread.NodeID), thread.ID)
	s.ZRem(nodeThreadsViewsIdx(thread.NodeID), thread.ID)
	s.ZRem(IdxLatestThreads, thread.ID)
	s.ZRem(userThreadsIdx(thread.UserID), thread.ID)
	for _, sortKey := range []string{"last", "new", "posts", "views"} {
		s.ZRem(nodeThreadsRankIdx(thread.NodeID, sortKey, true), thread.ID)
		s.ZRem(nodeThreadsRankIdx(thread.NodeID, sortKey, false), thread.ID)
	}
	for _, tagID := range thread.TagIDs {
		s.ZRem(tagThreadsIdx(tagID), thread.ID)
	}
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
	if thread.Status == domain.ThreadDeleted || thread.DiscussionState != domain.DiscussionVisible {
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
