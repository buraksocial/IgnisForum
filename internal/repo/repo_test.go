package repo

import (
	"strconv"
	"testing"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// openTestStore opens a real embedded engine in t.TempDir (mocks are forbidden
// by §11 — every test runs the real Rust-core LSM path). It returns the store
// and its data dir so tests can close + reopen the same directory.
func openTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(store.OpenConfig{DataDir: dir, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// TestThreadLifecycle verifies the M1 write path end to end: thread + reply
// create primaries, indexes, denormalised node counters and FT documents, and
// every listing primitive (ZPage/ZOldest) returns the right rows.
func TestThreadLifecycle(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)

	cat, err := r.Nodes.CreateCategory("Genel", "genel", "")
	if err != nil {
		t.Fatalf("category: %v", err)
	}
	node, err := r.Nodes.CreateForum(cat.ID, "Sohbet", "sohbet", "", false)
	if err != nil {
		t.Fatalf("forum: %v", err)
	}

	th, post, err := r.Threads.Create(node.ID, 1, "İlk konu", "merhaba [b]dünya[/b]")
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	rep, err := r.Posts.Create(th.ID, 2, "ilk yanıt")
	if err != nil {
		t.Fatalf("reply: %v", err)
	}

	// Node counters denormalised: 1 thread, 2 posts.
	fn, ok := r.Nodes.Get(node.ID)
	if !ok {
		t.Fatal("forum node missing")
	}
	if fn.ThreadCount != 1 || fn.PostCount != 2 {
		t.Errorf("node counters = %+v, want ThreadCount=1 PostCount=2", fn)
	}
	if fn.LastPost == nil || fn.LastPost.PostID != rep.ID {
		t.Errorf("node last-post = %+v, want post %d", fn.LastPost, rep.ID)
	}

	// Forum thread list: one thread, newest first.
	if ids := r.Threads.List(node.ID, "last", 0, 10); len(ids) != 1 || ids[0] != th.ID {
		t.Errorf("List(last) = %v, want [%d]", ids, th.ID)
	}

	// Thread posts: chronological, both messages present.
	pids := r.Posts.List(th.ID, 0, 10)
	if len(pids) != 2 || pids[0] != post.ID || pids[1] != rep.ID {
		t.Errorf("Posts.List = %v, want [%d %d]", pids, post.ID, rep.ID)
	}

	// Global latest index contains the thread.
	if ids := s.ZTop(IdxLatestThreads, 10); len(ids) != 1 || ids[0] != th.ID {
		t.Errorf("latest threads = %v, want [%d]", ids, th.ID)
	}

	// Thread state after reply: count 2, last-post = reply.
	got, ok := r.Threads.Get(th.ID)
	if !ok {
		t.Fatal("thread missing")
	}
	if got.PostCount != 2 || got.LastPost.PostID != rep.ID {
		t.Errorf("thread after reply = %+v, want PostCount=2 last=%d", got, rep.ID)
	}

	// FT: searching a term from the reply text finds the reply post.
	hits := s.FTSearch("yanıt", 10)
	wantDoc := "post:" + strconv.FormatUint(rep.ID, 10)
	if len(hits) != 1 || hits[0].DocID != wantDoc {
		t.Errorf("FTSearch = %+v, want one hit %s", hits, wantDoc)
	}
	if err := r.Posts.DeleteReply(post.ID); err == nil {
		t.Fatal("first post deletion was accepted")
	}
	if err := r.Posts.DeleteReply(rep.ID); err != nil {
		t.Fatalf("delete reply: %v", err)
	}
	if got, ok := r.Posts.Get(rep.ID); !ok || got.State != 2 {
		t.Fatalf("deleted post state=%+v found=%v", got, ok)
	}
	if ids := r.Posts.List(th.ID, 0, 10); len(ids) != 1 || ids[0] != post.ID {
		t.Fatalf("post index after deletion = %v", ids)
	}
	if hits := s.FTSearch("yanıt", 10); len(hits) != 0 {
		t.Fatalf("deleted reply remained searchable: %+v", hits)
	}
	got, _ = r.Threads.Get(th.ID)
	fn, _ = r.Nodes.Get(node.ID)
	if got.PostCount != 1 || got.LastPost.PostID != post.ID || fn.PostCount != 1 || fn.LastPost.PostID != post.ID {
		t.Fatalf("counters after deletion: thread=%+v forum=%+v", got, fn)
	}
	// Startup repair must rederive thread/node counts and last-post pointers
	// from active primaries after a crash between primary and counter writes.
	got.PostCount = 77
	got.LastPost = &domain.LastPostRef{PostID: rep.ID}
	if err := s.PutJSON(keyThread(th.ID), got); err != nil {
		t.Fatal(err)
	}
	s.RebuildContent(r.Hooks)
	if err := r.Nodes.RebuildContentCounts(); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Threads.Get(th.ID)
	if got.PostCount != 1 || got.LastPost.PostID != post.ID {
		t.Fatalf("rebuilt thread state=%+v", got)
	}

	// Tree blob: category → forum visible.
	tree, err := r.Nodes.LoadTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(tree.Categories) != 1 || tree.Categories[0].NodeID != cat.ID ||
		len(tree.Categories[0].Forums) != 1 || tree.Categories[0].Forums[0].NodeID != node.ID {
		t.Errorf("tree = %+v, want one category with the forum", tree)
	}
}

func TestThreadTagsRebuildIndexes(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)
	cat, err := r.Nodes.CreateCategory("Tags", "tags", "")
	if err != nil {
		t.Fatal(err)
	}
	forum, err := r.Nodes.CreateForum(cat.ID, "Etiketli", "etiketli", "", false)
	if err != nil {
		t.Fatal(err)
	}
	thread, _, err := r.Threads.Create(forum.ID, 3, "Go performans", "mesaj")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Tags.ApplyNames(thread.ID, []string{"Go", "performans", "Go"}); err != nil {
		t.Fatal(err)
	}
	tag, ok := r.Tags.BySlug("go")
	if !ok || tag.ThreadCount != 1 {
		t.Fatalf("tag lookup=%+v found=%v", tag, ok)
	}
	if ids := r.Tags.List("go", 0, 10); len(ids) != 1 || ids[0] != thread.ID {
		t.Fatalf("tag thread index=%v", ids)
	}
	s.RebuildContent(r.Hooks)
	tag, ok = r.Tags.BySlug("go")
	if !ok || tag.ThreadCount != 1 {
		t.Fatalf("rebuilt tag lookup=%+v found=%v", tag, ok)
	}
	if ids := r.Tags.List("go", 0, 10); len(ids) != 1 || ids[0] != thread.ID {
		t.Fatalf("rebuilt tag index=%v", ids)
	}
}

func TestPollVotesAreUniqueAndRebuildable(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)
	cat, err := r.Nodes.CreateCategory("Polls", "polls", "")
	if err != nil {
		t.Fatal(err)
	}
	forum, err := r.Nodes.CreateForum(cat.ID, "Anketler", "anketler", "", true)
	if err != nil {
		t.Fatal(err)
	}
	thread, _, err := r.Threads.Create(forum.ID, 1, "Soru", "Anket konusu")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Polls.Create(thread.ID, "Hangi dili tercih edersiniz?", []string{"Go", "Rust"}, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Polls.Vote(thread.ID, 7, []int{1}); err != nil {
		t.Fatal(err)
	}
	if err := r.Polls.Vote(thread.ID, 7, []int{0}); err == nil {
		t.Fatal("duplicate vote was accepted")
	}
	poll, ok := r.Polls.Get(thread.ID)
	if !ok || poll.Options[1].Votes != 1 || poll.Options[0].Votes != 0 {
		t.Fatalf("poll counts=%+v", poll)
	}
	s.RebuildContent(r.Hooks)
	poll, ok = r.Polls.Get(thread.ID)
	if !ok || poll.Options[1].Votes != 1 || poll.Options[0].Votes != 0 {
		t.Fatalf("rebuilt poll counts=%+v", poll)
	}
	if choices, found := r.Polls.UserVote(thread.ID, 7); !found || len(choices) != 1 || choices[0] != 1 {
		t.Fatalf("rebuilt ballot=%v, found=%v", choices, found)
	}
}

func TestReplyNotificationUnreadIndexRebuild(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)
	cat, err := r.Nodes.CreateCategory("Notify", "notify", "")
	if err != nil {
		t.Fatal(err)
	}
	forum, err := r.Nodes.CreateForum(cat.ID, "Notify", "notify-forum", "", false)
	if err != nil {
		t.Fatal(err)
	}
	thread, _, err := r.Threads.Create(forum.ID, 11, "Owner thread", "first")
	if err != nil {
		t.Fatal(err)
	}
	post, err := r.Posts.Create(thread.ID, 12, "a reply")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Notifications.UnreadCount(11); got != 1 {
		t.Fatalf("unread count=%d", got)
	}
	ids := r.Notifications.List(11, 0, 10)
	if len(ids) != 1 {
		t.Fatalf("notification ids=%v", ids)
	}
	item, ok := r.Notifications.Get(ids[0])
	if !ok || item.ActorID != 12 || item.PostID != post.ID {
		t.Fatalf("notification=%+v", item)
	}
	s.RebuildContent(r.Hooks)
	if got := r.Notifications.UnreadCount(11); got != 1 {
		t.Fatalf("rebuilt unread count=%d", got)
	}
	if err := r.Notifications.MarkRead(11, ids[0]); err != nil {
		t.Fatal(err)
	}
	if got := r.Notifications.UnreadCount(11); got != 0 {
		t.Fatalf("unread after read=%d", got)
	}
	s.RebuildContent(r.Hooks)
	if got := r.Notifications.UnreadCount(11); got != 0 {
		t.Fatalf("read state lost on rebuild, unread=%d", got)
	}
	if err := r.Notifications.MarkRead(99, ids[0]); err == nil {
		t.Fatal("another member marked notification read")
	}
}

func TestReactionToggleAndRebuild(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)
	cat, err := r.Nodes.CreateCategory("Genel", "genel", "")
	if err != nil {
		t.Fatal(err)
	}
	node, err := r.Nodes.CreateForum(cat.ID, "Sohbet", "sohbet", "", false)
	if err != nil {
		t.Fatal(err)
	}
	thread, post, err := r.Threads.Create(node.ID, 1, "Konu", "Mesaj")
	if err != nil {
		t.Fatal(err)
	}

	if got, err := r.Reactions.Toggle(post.ID, 2, "like"); err != nil || got != "like" {
		t.Fatalf("add reaction = %q, %v", got, err)
	}
	if got, ok := r.Reactions.Get(post.ID, 2); !ok || got != "like" {
		t.Fatalf("Get = %q, %v; want like", got, ok)
	}
	if got, err := r.Reactions.Toggle(post.ID, 2, "love"); err != nil || got != "love" {
		t.Fatalf("change reaction = %q, %v", got, err)
	}
	if got, err := r.Reactions.Toggle(post.ID, 2, "love"); err != nil || got != "" {
		t.Fatalf("remove reaction = %q, %v", got, err)
	}
	if _, ok := r.Reactions.Get(post.ID, 2); ok {
		t.Fatal("reaction still present after removal")
	}
	if _, err := r.Reactions.Toggle(post.ID, 3, "laugh"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reactions.Toggle(post.ID, 2, "not-allowed"); err == nil {
		t.Fatal("invalid reaction accepted")
	}

	if err := r.Reactions.r.S.PutJSON(keyPost(post.ID), &domain.Post{ID: post.ID, ThreadID: thread.ID, UserID: post.UserID}); err != nil {
		t.Fatal(err)
	}
	rep := s.RebuildContent(r.Hooks)
	if rep.Recovered == 0 {
		t.Fatal("rebuild recovered no records")
	}
	got, ok := r.Posts.Get(post.ID)
	if !ok || got.ReactionCounts["laugh"] != 1 || len(got.ReactionCounts) != 1 {
		t.Fatalf("rebuilt reaction counts = %#v", got.ReactionCounts)
	}
	if kind, ok := r.Reactions.Get(post.ID, 3); !ok || kind != "laugh" {
		t.Fatalf("rebuilt reaction lookup = %q, %v", kind, ok)
	}
	if s.ZCard(userLikedKey(2)) != 0 || s.ZCard(userLikedKey(3)) != 1 {
		t.Fatal("user reaction indexes not rebuilt")
	}
}

// indexSnapshot captures every member + score of an index for diffing.
func indexSnapshot(s *store.Store, key string) map[uint64]float64 {
	members := s.ZOldest(key, 0, 1<<30)
	out := make(map[uint64]float64, len(members))
	for _, m := range members {
		sc, _ := s.ZScoreOf(key, m)
		out[m] = sc
	}
	return out
}

func assertIndexEq(t *testing.T, key string, a, b map[uint64]float64) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: %d members before rebuild, %d after", key, len(a), len(b))
	}
	for m, sc := range a {
		bsc, ok := b[m]
		if !ok {
			t.Errorf("%s: member %d missing after rebuild", key, m)
			continue
		}
		if sc != bsc {
			t.Errorf("%s: member %d score %v, want %v after rebuild", key, m, bsc, sc)
		}
	}
}

// TestRebuildContentTenK is the §11 scale test: ~10K write operations through
// the real repo path, then close + reopen (indexes are RAM-only and vanish) +
// RebuildContent, then a full index-by-index diff: every derived index must be
// identical (members AND scores) to the pre-rebuild state.
func TestRebuildContentTenK(t *testing.T) {
	s, dir := openTestStore(t)
	r := New(s)

	// Structure: 2 categories, 2 forums (4 node ops).
	catA, err := r.Nodes.CreateCategory("Genel", "genel", "")
	if err != nil {
		t.Fatal(err)
	}
	catB, err := r.Nodes.CreateCategory("Teknik", "teknik", "")
	if err != nil {
		t.Fatal(err)
	}
	fA, err := r.Nodes.CreateForum(catA.ID, "Sohbet", "sohbet", "", false)
	if err != nil {
		t.Fatal(err)
	}
	fB, err := r.Nodes.CreateForum(catB.ID, "Go", "go", "", true)
	if err != nil {
		t.Fatal(err)
	}

	ops := 4 // node writes
	nodes := []uint64{fA.ID, fB.ID}
	var threadIDs []uint64
	for ops < 10000 {
		nodeID := nodes[len(threadIDs)%len(nodes)]
		uid := uint64(1 + len(threadIDs)%3)
		th, _, err := r.Threads.Create(nodeID, uid, "konu-"+strconv.Itoa(len(threadIDs)+1), "mesaj içeriği go performans")
		if err != nil {
			t.Fatalf("thread %d: %v", len(threadIDs), err)
		}
		threadIDs = append(threadIDs, th.ID)
		ops += 2 // thread + first post
		if ops < 10000 {
			if _, err := r.Posts.Create(th.ID, uid, "yanıt"); err != nil {
				t.Fatalf("reply %d: %v", len(threadIDs), err)
			}
			ops++
		}
	}
	if ops < 10000 {
		t.Fatalf("only %d ops", ops)
	}
	t.Logf("seeded %d threads, %d ops across 2 forums", len(threadIDs), ops)

	// Snapshot every derived index BEFORE the restart.
	keys := []string{
		childrenOf(0), childrenOf(catA.ID), childrenOf(catB.ID),
		IdxLatestThreads, IdxLatestPosts,
		NodeThreadsKey(fA.ID), NodeThreadsKey(fB.ID),
		nodeThreadsNewIdx(fA.ID), nodeThreadsNewIdx(fB.ID),
		nodeThreadsPostsIdx(fA.ID), nodeThreadsPostsIdx(fB.ID),
		nodeThreadsViewsIdx(fA.ID), nodeThreadsViewsIdx(fB.ID),
		userThreadsIdx(1), userThreadsIdx(2), userThreadsIdx(3),
		userPostsIdx(1), userPostsIdx(2), userPostsIdx(3),
	}
	for _, id := range threadIDs {
		keys = append(keys, ThreadPostsKey(id))
	}
	before := make(map[string]map[uint64]float64, len(keys))
	for _, k := range keys {
		before[k] = indexSnapshot(s, k)
	}
	if got := len(before[NodeThreadsKey(fA.ID)]) + len(before[NodeThreadsKey(fB.ID)]); got != len(threadIDs) {
		t.Fatalf("thread index cardinality %d != %d threads", got, len(threadIDs))
	}

	// Close: WAL/SSTable durable, RAM-only ZSETs + FT vanish.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := store.Open(store.OpenConfig{DataDir: dir, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	r2 := New(s2)
	rep := s2.RebuildContent(r2.Hooks)
	t.Logf("rebuild: scanned=%d recovered=%d cleared=%d in %.3fs",
		rep.Scanned, rep.Recovered, rep.Cleared, rep.Elapsed)

	// Diff every index.
	for _, k := range keys {
		assertIndexEq(t, k, before[k], indexSnapshot(s2, k))
	}

	// Primary integrity: a sample thread is intact after the restart.
	if th, ok := r2.Threads.Get(threadIDs[42]); !ok || th.PostCount < 1 {
		t.Errorf("thread 42 after rebuild: ok=%v", ok)
	}
}
