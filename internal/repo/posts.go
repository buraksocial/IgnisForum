package repo

import (
	"fmt"
	"time"

	"github.com/buraksocial/IgnisForum/internal/bbcode"
	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// PostRepo owns post creation and the indexes that make "latest posts", "thread
// messages" and "user posts" O(k) reads.
type PostRepo struct {
	r *Repos
}

// Create adds a reply to a thread. Order: post primary, indexes, thread bump,
// node bump, FT.
func (p *PostRepo) Create(threadID, userID uint64, rawMessage string) (*domain.Post, error) {
	s := p.r.S
	thread, ok := p.r.Threads.Get(threadID)
	if !ok {
		return nil, fmt.Errorf("thread %d not found", threadID)
	}
	now := time.Now().UTC()

	postID := s.NextID()
	s.MarkID(store.TypePost, postID)

	post := &domain.Post{
		ID: postID, ThreadID: threadID, UserID: userID,
		MessageRaw: rawMessage, MessageRendered: bbcode.Render(rawMessage),
		Date: now, State: 0,
	}

	// 1. PRIMARY FIRST.
	if err := s.PutJSON(keyPost(postID), post); err != nil {
		return nil, fmt.Errorf("put post: %w", err)
	}

	// 2. INDEXES.
	p.indexPost(post)

	// 3. THREAD + NODE COUNTERS.
	if err := p.r.Threads.BumpThread(threadID, postID, userID, now); err != nil {
		return nil, err
	}
	p.bumpNode(thread.NodeID, now, postID, userID, false)

	// 4. FT.
	s.FTAdd(fmt.Sprintf("post:%d", postID), thread.Title+" "+post.MessageRendered)

	return post, nil
}

// Get returns a post by id.
func (p *PostRepo) Get(id uint64) (*domain.Post, bool) {
	var post domain.Post
	ok, _ := p.r.S.GetJSON(keyPost(id), &post)
	if !ok {
		return nil, false
	}
	return &post, true
}

// List returns a page of post IDs in a thread, oldest first (chronological).
func (p *PostRepo) List(threadID uint64, offset, n int) []uint64 {
	return p.r.S.ZOldest(ThreadPostsKey(threadID), offset, n)
}

// indexPost adds a post to the thread, latest-posts and user-posts indexes.
func (p *PostRepo) indexPost(post *domain.Post) {
	s := p.r.S
	s.ZAddIdx(ThreadPostsKey(post.ThreadID), ts(post.Date), post.ID)
	s.ZAddIdx(IdxLatestPosts, ts(post.Date), post.ID)
	s.ZAddIdx(userPostsIdx(post.UserID), ts(post.Date), post.ID)
}

// bumpNode updates the node's denormalised counters/last-post (single RMW blob).
// isThreadStart adds the thread counter in the same RMW (thread creation is a
// post too — one read-modify-write covers both).
func (p *PostRepo) bumpNode(nodeID uint64, when time.Time, postID, userID uint64, isThreadStart bool) {
	s := p.r.S
	var node domain.Node
	if _, err := s.GetJSON(keyNode(nodeID), &node); err != nil {
		return
	}
	node.PostCount++
	if isThreadStart {
		node.ThreadCount++
	}
	if node.LastPost == nil || when.After(node.LastPost.T) {
		node.LastPost = &domain.LastPostRef{T: when, PostID: postID, UserID: userID}
	}
	_ = s.PutJSON(keyNode(nodeID), &node)
}

// rebuildHook re-derives a post's index entries + FT from its blob. The thread
// Get is a cache hit (the thread phase ran earlier in the same walk).
func (p *PostRepo) rebuildHook(key string, blob []byte) {
	s := p.r.S
	var post domain.Post
	if err := s.DecodeJSON(blob, &post); err != nil {
		return
	}
	if post.State == 2 { // deleted
		return
	}
	p.indexPost(&post)
	if th, ok := p.r.Threads.Get(post.ThreadID); ok {
		s.FTAdd(fmt.Sprintf("post:%d", post.ID), th.Title+" "+post.MessageRendered)
	}
}
