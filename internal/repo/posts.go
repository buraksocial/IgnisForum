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

// Edit replaces a post body while retaining its original chronological date.
// The store has a single writer, so read-modify-write and index refresh happen
// serially with all other forum mutations.
func (p *PostRepo) Edit(id uint64, message string) (*domain.Post, error) {
	post, ok := p.Get(id)
	if !ok || post.State != 0 {
		return nil, fmt.Errorf("post %d not found or not editable", id)
	}
	post.MessageRaw = message
	post.MessageRendered = bbcode.Render(message)
	post.EditCount++
	post.EditedAt = time.Now().UTC()
	post.Version++
	if err := p.r.S.PutJSON(keyPost(id), post); err != nil {
		return nil, err
	}
	if thread, ok := p.r.Threads.Get(post.ThreadID); ok {
		p.r.S.FTAdd(fmt.Sprintf("post:%d", post.ID), thread.Title+" "+post.MessageRendered)
	}
	return post, nil
}

// DeleteReply soft-deletes a reply without allowing the first post (which owns
// the discussion) to be orphaned. Primary state is written before derived
// indexes so a restart can repair an interrupted index update.
func (p *PostRepo) DeleteReply(id uint64) error {
	post, ok := p.Get(id)
	if !ok || post.State != 0 {
		return fmt.Errorf("post %d not found or already deleted", id)
	}
	thread, ok := p.r.Threads.Get(post.ThreadID)
	if !ok || thread.FirstPostID == id {
		return fmt.Errorf("the first post of a discussion cannot be deleted")
	}
	post.State = 2
	post.Version++
	if err := p.r.S.PutJSON(keyPost(id), post); err != nil {
		return err
	}
	p.r.S.ZRem(ThreadPostsKey(post.ThreadID), id)
	p.r.S.ZRem(IdxLatestPosts, id)
	p.r.S.ZRem(userPostsIdx(post.UserID), id)
	p.r.S.FTRemove(fmt.Sprintf("post:%d", id))

	thread.PostCount--
	if thread.PostCount < 1 {
		thread.PostCount = 1
	}
	if thread.LastPost != nil && thread.LastPost.PostID == id {
		count := p.r.S.ZCard(ThreadPostsKey(thread.ID))
		if count > 0 {
			ids := p.r.S.ZOldest(ThreadPostsKey(thread.ID), count-1, 1)
			if len(ids) == 1 {
				if last, found := p.Get(ids[0]); found {
					thread.LastPost = &domain.LastPostRef{T: last.Date, PostID: last.ID, UserID: last.UserID}
				}
			}
		}
	}
	thread.Version++
	if err := p.r.S.PutJSON(keyThread(thread.ID), thread); err != nil {
		return err
	}
	p.r.Threads.indexThread(thread)

	if node, found := p.r.Nodes.Get(thread.NodeID); found {
		if node.PostCount > 0 {
			node.PostCount--
		}
		if node.LastPost != nil && node.LastPost.PostID == id {
			latest := p.r.Threads.List(node.ID, "last", 0, 1)
			node.LastPost = nil
			if len(latest) == 1 {
				if lastThread, found := p.r.Threads.Get(latest[0]); found {
					node.LastPost = lastThread.LastPost
				}
			}
		}
		if err := p.r.S.PutJSON(keyNode(node.ID), node); err != nil {
			return err
		}
	}
	return nil
}

// List returns a page of post IDs in a thread, oldest first (chronological).
func (p *PostRepo) List(threadID uint64, offset, n int) []uint64 {
	return p.r.S.ZOldest(ThreadPostsKey(threadID), offset, n)
}

// RecentByUser returns the newest indexed posts for a member, newest first.
func (p *PostRepo) RecentByUser(userID uint64, n int) []uint64 {
	return p.r.S.ZTop(userPostsIdx(userID), n)
}

func (p *PostRepo) CountByUser(userID uint64) int { return p.r.S.ZCard(userPostsIdx(userID)) }

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
	// Reaction totals are derived from reaction primary records, which are
	// visited after posts during rebuild.
	post.ReactionCounts = nil
	_ = s.PutJSON(keyPost(post.ID), &post)
	if post.State == 2 { // deleted
		return
	}
	p.indexPost(&post)
	if th, ok := p.r.Threads.Get(post.ThreadID); ok {
		s.FTAdd(fmt.Sprintf("post:%d", post.ID), th.Title+" "+post.MessageRendered)
	}
}
