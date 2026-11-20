// Package repo is the single write point for forum entities (§5). Every create /
// update follows the same discipline: primary record FIRST, then derived ZSET
// indexes, then FT, then denormalised counters — so a crash can only leave
// indexes/FT short (RebuildContent repairs), never a half-written primary.
//
// Reading is always via store.ZPage / store.GetJSON; no prefix scan exists.
package repo

import (
	"fmt"
	"sync"
	"time"

	"github.com/buraksocial/IgnisForum/internal/store"
)

// Repos bundles the per-entity services over one store.
type Repos struct {
	S          *store.Store
	Nodes      *NodeRepo
	Threads    *ThreadRepo
	Posts      *PostRepo
	Reactions  *ReactionRepo
	Tags       *TagRepo
	reactionMu sync.Mutex

	// Hooks is the store.RebuildHooks set; the server passes it to
	// store.RebuildContent at startup and from the admin "Rebuild Content" tool.
	Hooks *store.RebuildHooks
}

// New builds the repository set and registers the rebuild hooks with the store.
func New(s *store.Store) *Repos {
	r := &Repos{
		S:         s,
		Nodes:     &NodeRepo{r: nil},
		Threads:   &ThreadRepo{r: nil},
		Posts:     &PostRepo{r: nil},
		Reactions: &ReactionRepo{r: nil},
		Tags:      &TagRepo{r: nil},
	}
	r.Nodes.r = r
	r.Threads.r = r
	r.Posts.r = r
	r.Reactions.r = r
	r.Tags.r = r

	hooks := store.NewRebuildHooks()
	hooks.Set(store.TypeNode, r.Nodes.rebuildHook)
	hooks.Set(store.TypeThread, r.Threads.rebuildHook)
	hooks.Set(store.TypePost, r.Posts.rebuildHook)
	hooks.Set(store.TypePostReaction, r.Reactions.rebuildHook)
	hooks.Set(store.TypeReport, r.rebuildReportHook)
	hooks.Set(store.TypeTag, r.Tags.rebuildHook)
	r.Hooks = hooks
	return r
}

// idx names (derived ZSETs, §4).
const (
	IdxChildren         = "idx:children"         // member=nodeID, score=position
	IdxNodeThreads      = "idx:node_threads"     // member=threadID, score=last-post time
	IdxNodeThreadsNew   = "idx:node_threads_new" // score=thread creation time
	IdxNodeThreadsVoted = "idx:node_threads_voted"
	IdxNodeThreadsViews = "idx:node_threads_views"
	IdxNodeThreadsPosts = "idx:node_threads_posts"
	IdxLatestThreads    = "idx:latest_threads" // global "son konular"
	IdxLatestPosts      = "idx:latest_posts"   // global "son mesajlar"
	IdxUserThreads      = "idx:user_threads"
	IdxUserPosts        = "idx:user_posts"
	IdxThreadPosts      = "idx:thread_posts"
	IdxUserLiked        = "idx:user_liked"
	IdxTagThreads       = "idx:tag_threads"
	IdxTagSlug          = "idx:tag_slug"
)

// NodeThreadsKey / ThreadPostsKey expose the per-entity index names so the web
// layer (and tests) reference the key schema from one place.
func NodeThreadsKey(nodeID uint64) string { return fmt.Sprintf("%s:%d", IdxNodeThreads, nodeID) }
func ThreadPostsKey(threadID uint64) string {
	return fmt.Sprintf("%s:%d", IdxThreadPosts, threadID)
}

func nodeThreadsNewIdx(nodeID uint64) string { return fmt.Sprintf("%s:%d", IdxNodeThreadsNew, nodeID) }
func nodeThreadsVotedIdx(nodeID uint64) string {
	return fmt.Sprintf("%s:%d", IdxNodeThreadsVoted, nodeID)
}
func nodeThreadsViewsIdx(nodeID uint64) string {
	return fmt.Sprintf("%s:%d", IdxNodeThreadsViews, nodeID)
}
func nodeThreadsPostsIdx(nodeID uint64) string {
	return fmt.Sprintf("%s:%d", IdxNodeThreadsPosts, nodeID)
}
func userThreadsIdx(uid uint64) string { return fmt.Sprintf("%s:%d", IdxUserThreads, uid) }
func userPostsIdx(uid uint64) string   { return fmt.Sprintf("%s:%d", IdxUserPosts, uid) }
func tagThreadsIdx(id uint64) string   { return fmt.Sprintf("%s:%d", IdxTagThreads, id) }
func childrenOf(parent uint64) string  { return fmt.Sprintf("%s:%d", IdxChildren, parent) }

func keyNode(id uint64) string   { return fmt.Sprintf("node:%d", id) }
func keyThread(id uint64) string { return fmt.Sprintf("thread:%d", id) }
func keyPost(id uint64) string   { return fmt.Sprintf("post:%d", id) }
func keyThreadUserState(threadID, userID uint64) string {
	return fmt.Sprintf("thread_user:%d:%d", threadID, userID)
}

// ts converts time to a float64 score (unix seconds; exact for our range).
func ts(t time.Time) float64 { return float64(t.Unix()) }
