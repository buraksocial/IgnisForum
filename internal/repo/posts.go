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
	return p.CreateWithApproval(threadID, userID, rawMessage, true)
}

// CreateWithApproval stores a pending reply when the caller lacks the
// without-approval permission. Pending posts remain primary records but never
// enter public indexes, full-text search, counters, or notifications.
func (p *PostRepo) CreateWithApproval(threadID, userID uint64, rawMessage string, approved bool) (*domain.Post, error) {
	s := p.r.S
	thread, ok := p.r.Threads.Get(threadID)
	if !ok {
		return nil, fmt.Errorf("thread %d not found", threadID)
	}
	if thread.Status != domain.ThreadOpen || thread.DiscussionState != domain.DiscussionVisible {
		return nil, fmt.Errorf("thread %d is not accepting replies", threadID)
	}
	now := time.Now().UTC()

	postID := s.NextID()
	s.MarkID(store.TypePost, postID)

	post := &domain.Post{
		ID: postID, ThreadID: threadID, UserID: userID,
		MessageRaw: rawMessage, MessageRendered: bbcode.Render(rawMessage),
		Date: now, State: domain.PostVisible,
	}
	if !approved {
		post.State = domain.PostPending
	}

	// 1. PRIMARY FIRST.
	if err := s.PutJSON(keyPost(postID), post); err != nil {
		return nil, fmt.Errorf("put post: %w", err)
	}

	// 2. INDEXES.
	p.indexPost(post)

	if post.State == domain.PostPending {
		return post, nil
	}

	// 3. THREAD + NODE COUNTERS.
	if err := p.r.Threads.BumpThread(threadID, postID, userID, now); err != nil {
		return nil, err
	}
	p.bumpNode(thread.NodeID, now, postID, userID, false)

	// 4. FT.
	s.FTAdd(fmt.Sprintf("post:%d", postID), thread.Title+" "+post.MessageRendered)
	// Notify the discussion owner after the durable reply exists. Notification
	// indexing is derived from its primary record and repaired at startup.
	_ = p.r.Notifications.Create(thread.UserID, userID, threadID, postID)

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
	if thread, ok := p.r.Threads.Get(post.ThreadID); ok &&
		thread.DiscussionState == domain.DiscussionVisible && thread.Status != domain.ThreadDeleted {
		p.r.S.FTAdd(fmt.Sprintf("post:%d", post.ID), thread.Title+" "+post.MessageRendered)
	} else {
		p.r.S.FTRemove(fmt.Sprintf("post:%d", post.ID))
	}
	return post, nil
}

// DeleteReply soft-deletes a reply without allowing the first post (which owns
// the discussion) to be orphaned. Primary state is written before derived
// indexes so a restart can repair an interrupted index update.
func (p *PostRepo) DeleteReply(id uint64) error {
	post, ok := p.Get(id)
	if !ok || post.State != domain.PostVisible {
		return fmt.Errorf("post %d not found or already deleted", id)
	}
	thread, ok := p.r.Threads.Get(post.ThreadID)
	if !ok || thread.FirstPostID == id {
		return fmt.Errorf("the first post of a discussion cannot be deleted")
	}
	wasPublic := thread.DiscussionState == domain.DiscussionVisible && thread.Status != domain.ThreadDeleted
	post.State = domain.PostDeleted
	post.Version++
	if err := p.r.S.PutJSON(keyPost(id), post); err != nil {
		return err
	}
	p.r.S.ZRem(ThreadPostsKey(post.ThreadID), id)
	p.r.S.ZRem(IdxPendingPosts, id)
	p.r.S.ZRem(IdxLatestPosts, id)
	p.r.S.ZRem(userPostsIdx(post.UserID), id)
	p.r.S.FTRemove(fmt.Sprintf("post:%d", id))

	thread.PostCount--
	if thread.PostCount < 1 {
		thread.PostCount = 1
	}
	if thread.LastPost != nil && thread.LastPost.PostID == id {
		thread.LastPost = p.latestVisibleInThread(thread.ID)
	}
	thread.Version++
	if err := p.r.S.PutJSON(keyThread(thread.ID), thread); err != nil {
		return err
	}
	p.r.Threads.indexThread(thread)

	if node, found := p.r.Nodes.Get(thread.NodeID); found && wasPublic {
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

// Pending returns post IDs awaiting moderator approval, newest first.
func (p *PostRepo) Pending(offset, n int) []uint64 {
	return p.r.S.ZPage(IdxPendingPosts, offset, n)
}

// Approve publishes one pending post and updates all derived indexes/counters.
func (p *PostRepo) Approve(id uint64) error {
	post, ok := p.Get(id)
	if !ok || post.State != domain.PostPending {
		return fmt.Errorf("post %d is not pending", id)
	}
	thread, ok := p.r.Threads.Get(post.ThreadID)
	if !ok || thread.DiscussionState == domain.DiscussionHidden || thread.Status == domain.ThreadDeleted {
		return fmt.Errorf("thread %d not found", post.ThreadID)
	}
	if thread.DiscussionState == domain.DiscussionPending && post.ID != thread.FirstPostID {
		return fmt.Errorf("the first post must be approved before replies")
	}
	post.State = domain.PostVisible
	post.Version++
	if err := p.r.S.PutJSON(keyPost(id), post); err != nil {
		return err
	}
	p.r.S.ZRem(IdxPendingPosts, id)
	if post.ID == thread.FirstPostID && thread.DiscussionState == domain.DiscussionPending {
		thread.DiscussionState = domain.DiscussionVisible
		if err := p.r.S.PutJSON(keyThread(thread.ID), thread); err != nil {
			return err
		}
		p.RefreshThreadVisibility(thread)
		p.r.Threads.indexThread(thread)
	}
	p.indexPost(post)
	if thread.DiscussionState == domain.DiscussionVisible {
		p.r.S.FTAdd(fmt.Sprintf("post:%d", id), thread.Title+" "+post.MessageRendered)
	}
	if err := p.r.Threads.BumpThread(thread.ID, post.ID, post.UserID, post.Date); err != nil {
		return err
	}
	p.bumpNode(thread.NodeID, post.Date, post.ID, post.UserID, post.ID == thread.FirstPostID)
	_ = p.r.Notifications.Create(thread.UserID, post.UserID, thread.ID, post.ID)
	return nil
}

// Hide changes a visible post to the reversible hidden state. Deleted posts
// remain distinct and cannot be restored through moderation.
func (p *PostRepo) Hide(id uint64) error {
	post, ok := p.Get(id)
	if !ok || post.State != domain.PostVisible {
		return fmt.Errorf("post %d is not visible", id)
	}
	thread, ok := p.r.Threads.Get(post.ThreadID)
	if !ok || thread.FirstPostID == id {
		return fmt.Errorf("the first post cannot be hidden independently")
	}
	wasPublic := thread.DiscussionState == domain.DiscussionVisible && thread.Status != domain.ThreadDeleted
	post.State = domain.PostHidden
	post.Version++
	if err := p.r.S.PutJSON(keyPost(id), post); err != nil {
		return err
	}
	p.r.S.ZRem(IdxLatestPosts, id)
	p.r.S.ZRem(userPostsIdx(post.UserID), id)
	p.r.S.FTRemove(fmt.Sprintf("post:%d", id))
	thread.PostCount--
	if thread.PostCount < 1 {
		thread.PostCount = 1
	}
	if thread.LastPost != nil && thread.LastPost.PostID == id {
		thread.LastPost = p.latestVisibleInThread(thread.ID)
	}
	thread.Version++
	p.r.Threads.removeThreadIndexes(thread)
	if err := p.r.S.PutJSON(keyThread(thread.ID), thread); err != nil {
		return err
	}
	p.r.Threads.indexThread(thread)
	if node, found := p.r.Nodes.Get(thread.NodeID); found && wasPublic {
		if node.PostCount > 0 {
			node.PostCount--
		}
		if node.LastPost != nil && node.LastPost.PostID == id {
			node.LastPost = p.latestVisibleInNode(node.ID)
		}
		if err := p.r.S.PutJSON(keyNode(node.ID), node); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostRepo) Restore(id uint64) error {
	post, ok := p.Get(id)
	if !ok || post.State != domain.PostHidden {
		return fmt.Errorf("post %d is not hidden", id)
	}
	thread, ok := p.r.Threads.Get(post.ThreadID)
	if !ok || thread.DiscussionState != domain.DiscussionVisible || thread.Status == domain.ThreadDeleted {
		return fmt.Errorf("thread is not publicly visible")
	}
	post.State = domain.PostVisible
	post.Version++
	if err := p.r.S.PutJSON(keyPost(id), post); err != nil {
		return err
	}
	thread.PostCount++
	if thread.LastPost == nil || post.Date.After(thread.LastPost.T) {
		thread.LastPost = &domain.LastPostRef{T: post.Date, PostID: post.ID, UserID: post.UserID}
	}
	thread.Version++
	p.r.Threads.removeThreadIndexes(thread)
	if err := p.r.S.PutJSON(keyThread(thread.ID), thread); err != nil {
		return err
	}
	p.r.Threads.indexThread(thread)
	p.indexPost(post)
	p.r.S.FTAdd(fmt.Sprintf("post:%d", id), thread.Title+" "+post.MessageRendered)
	p.bumpNode(thread.NodeID, post.Date, post.ID, post.UserID, false)
	return nil
}

func (p *PostRepo) latestVisibleInThread(threadID uint64) *domain.LastPostRef {
	ids := p.r.S.ZPage(ThreadPostsKey(threadID), 0, p.r.S.ZCard(ThreadPostsKey(threadID)))
	for _, id := range ids {
		if post, ok := p.Get(id); ok && post.State == domain.PostVisible {
			return &domain.LastPostRef{T: post.Date, PostID: post.ID, UserID: post.UserID}
		}
	}
	return nil
}

func (p *PostRepo) latestVisibleInNode(nodeID uint64) *domain.LastPostRef {
	ids := p.r.Threads.List(nodeID, "last", 0, p.r.S.ZCard(NodeThreadsKey(nodeID)))
	for _, id := range ids {
		if thread, ok := p.r.Threads.Get(id); ok && thread.LastPost != nil {
			return thread.LastPost
		}
	}
	return nil
}

// SetClientIP stores only the existing one-way IP digest used by the
// moderator-only view; raw client addresses are never persisted.
func (p *PostRepo) SetClientIP(id uint64, digest string) error {
	post, ok := p.Get(id)
	if !ok {
		return fmt.Errorf("post %d not found", id)
	}
	post.IPHash = digest
	return p.r.S.PutJSON(keyPost(id), post)
}

func (p *PostRepo) indexPost(post *domain.Post) {
	s := p.r.S
	if post.State == domain.PostPending {
		s.ZAddIdx(IdxPendingPosts, ts(post.Date), post.ID)
		s.ZAddIdx(ThreadPostsKey(post.ThreadID), ts(post.Date), post.ID)
		return
	}
	if post.State == domain.PostHidden {
		s.ZAddIdx(ThreadPostsKey(post.ThreadID), ts(post.Date), post.ID)
		return
	}
	if post.State != domain.PostVisible {
		return
	}
	thread, ok := p.r.Threads.Get(post.ThreadID)
	if !ok || thread.Status == domain.ThreadDeleted {
		return
	}
	s.ZAddIdx(ThreadPostsKey(post.ThreadID), ts(post.Date), post.ID)
	if thread.DiscussionState != domain.DiscussionVisible {
		return
	}
	s.ZAddIdx(IdxLatestPosts, ts(post.Date), post.ID)
	s.ZAddIdx(userPostsIdx(post.UserID), ts(post.Date), post.ID)
}

// RefreshThreadVisibility updates public post indexes and search documents
// after the containing discussion's visibility has been persisted.
func (p *PostRepo) RefreshThreadVisibility(thread *domain.Thread) {
	ids := p.r.S.ZPage(ThreadPostsKey(thread.ID), 0, p.r.S.ZCard(ThreadPostsKey(thread.ID)))
	for _, id := range ids {
		post, ok := p.Get(id)
		if !ok || post.State != domain.PostVisible {
			continue
		}
		if thread.DiscussionState == domain.DiscussionVisible && thread.Status != domain.ThreadDeleted {
			p.r.S.ZAddIdx(IdxLatestPosts, ts(post.Date), post.ID)
			p.r.S.ZAddIdx(userPostsIdx(post.UserID), ts(post.Date), post.ID)
			p.r.S.FTAdd(fmt.Sprintf("post:%d", post.ID), thread.Title+" "+post.MessageRendered)
		} else {
			p.r.S.ZRem(IdxLatestPosts, post.ID)
			p.r.S.ZRem(userPostsIdx(post.UserID), post.ID)
			p.r.S.FTRemove(fmt.Sprintf("post:%d", post.ID))
		}
	}
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
	p.indexPost(&post)
	if post.State != domain.PostVisible {
		return
	}
	if th, ok := p.r.Threads.Get(post.ThreadID); ok && th.DiscussionState == domain.DiscussionVisible && th.Status != domain.ThreadDeleted {
		s.FTAdd(fmt.Sprintf("post:%d", post.ID), th.Title+" "+post.MessageRendered)
	}
}
