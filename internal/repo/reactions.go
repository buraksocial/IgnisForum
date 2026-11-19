package repo

import (
	"fmt"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// ReactionTypes is the allow-list shared by persistence and the SSR controls.
var ReactionTypes = []string{"like", "love", "laugh", "sad", "wow"}

// ReactionRepo stores one active reaction per (post, user) as a primary record.
type ReactionRepo struct{ r *Repos }

func reactionKey(id uint64) string { return fmt.Sprintf("reaction:%d", id) }
func reactionLookupKey(postID, userID uint64) string {
	return fmt.Sprintf("reaction:by_post_user:%d:%d", postID, userID)
}
func userLikedKey(userID uint64) string { return fmt.Sprintf("%s:%d", IdxUserLiked, userID) }

func validReactionType(kind string) bool {
	for _, candidate := range ReactionTypes {
		if kind == candidate {
			return true
		}
	}
	return false
}

// Get returns the current active reaction, if any.
func (rr *ReactionRepo) Get(postID, userID uint64) (string, bool) {
	id, ok := rr.r.S.GetRaw(reactionLookupKey(postID, userID))
	if !ok || len(id) == 0 {
		return "", false
	}
	var reaction domain.Reaction
	ok, err := rr.r.S.GetJSON(string(id), &reaction)
	if err != nil || !ok || reaction.State != 0 {
		return "", false
	}
	return reaction.Type, true
}

// Toggle adds, changes, or removes the caller's reaction. Writes are serialized
// in-process because the entity update and denormalized post counters span keys.
func (rr *ReactionRepo) Toggle(postID, userID uint64, kind string) (string, error) {
	if userID == 0 || !validReactionType(kind) {
		return "", fmt.Errorf("invalid reaction")
	}
	r := rr.r
	r.reactionMu.Lock()
	defer r.reactionMu.Unlock()
	post, ok := r.Posts.Get(postID)
	if !ok || post.State != 0 {
		return "", fmt.Errorf("post %d not found", postID)
	}
	lookup := reactionLookupKey(postID, userID)
	var reaction domain.Reaction
	var oldType string
	if id, exists := r.S.GetRaw(lookup); exists && len(id) > 0 {
		if found, err := r.S.GetJSON(string(id), &reaction); err != nil {
			return "", err
		} else if found && reaction.State == 0 {
			oldType = reaction.Type
		}
	}
	if oldType == kind {
		reaction.State = 2
		if err := r.S.PutJSON(reactionKey(reaction.ID), &reaction); err != nil {
			return "", err
		}
		r.S.Delete(lookup)
		r.S.ZRem(userLikedKey(userID), postID)
		adjustReactionCount(post, oldType, -1)
		if err := r.S.PutJSON(keyPost(postID), post); err != nil {
			return "", err
		}
		return "", nil
	}
	if oldType == "" {
		reaction.ID = r.S.NextID()
		reaction.PostID, reaction.UserID = postID, userID
		r.S.MarkID(store.TypePostReaction, reaction.ID)
	}
	reaction.Type, reaction.State = kind, 0
	if err := r.S.PutJSON(reactionKey(reaction.ID), &reaction); err != nil {
		return "", err
	}
	if err := r.S.PutRaw(lookup, []byte(reactionKey(reaction.ID))); err != nil {
		return "", err
	}
	r.S.ZAddIdx(userLikedKey(userID), float64(postID), postID)
	if oldType != "" {
		adjustReactionCount(post, oldType, -1)
	}
	adjustReactionCount(post, kind, 1)
	if err := r.S.PutJSON(keyPost(postID), post); err != nil {
		return "", err
	}
	return kind, nil
}

func adjustReactionCount(post *domain.Post, kind string, delta int) {
	if post.ReactionCounts == nil {
		post.ReactionCounts = make(map[string]int)
	}
	post.ReactionCounts[kind] += delta
	if post.ReactionCounts[kind] <= 0 {
		delete(post.ReactionCounts, kind)
	}
}

// rebuildHook reconstructs user indexes, lookup records, and the post's
// denormalized reaction totals from primary reaction records.
func (rr *ReactionRepo) rebuildHook(key string, blob []byte) {
	var reaction domain.Reaction
	if err := rr.r.S.DecodeJSON(blob, &reaction); err != nil {
		return
	}
	lookup := reactionLookupKey(reaction.PostID, reaction.UserID)
	if reaction.State != 0 {
		rr.r.S.Delete(lookup)
		return
	}
	if !validReactionType(reaction.Type) {
		return
	}
	rr.r.S.PutRaw(lookup, []byte(key))
	rr.r.S.ZAddIdx(userLikedKey(reaction.UserID), float64(reaction.PostID), reaction.PostID)
	var post domain.Post
	if ok, _ := rr.r.S.GetJSON(keyPost(reaction.PostID), &post); ok {
		adjustReactionCount(&post, reaction.Type, 1)
		_ = rr.r.S.PutJSON(keyPost(post.ID), &post)
	}
}
