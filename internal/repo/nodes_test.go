package repo

import (
	"testing"
)

// TestNodeMoveAndRebuild covers hierarchy mutation: MoveNode re-parents, the
// tree blob follows, and a rebuild walk re-derives the children index under the
// NEW parent.
func TestNodeMoveAndRebuild(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)

	a, _ := r.Nodes.CreateCategory("A", "a", "")
	b, _ := r.Nodes.CreateCategory("B", "b", "")
	f, _ := r.Nodes.CreateForum(a.ID, "Forum", "f", "", false)

	// Move the forum under category B.
	if err := r.Nodes.MoveNode(f.ID, b.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	tree, _ := r.Nodes.LoadTree()
	if len(tree.Categories) != 2 || len(tree.Categories[0].Forums) != 0 || len(tree.Categories[1].Forums) != 1 {
		t.Fatalf("tree after move = %+v, want forum under B only", tree)
	}

	// Rebuild re-derives idx:children:<parent> from the node blobs.
	rep := s.RebuildContent(r.Hooks)
	if rep.Recovered == 0 {
		t.Fatalf("nothing recovered: %+v", rep)
	}
	if s.ZCard(childrenOf(b.ID)) != 1 || s.ZCard(childrenOf(a.ID)) != 0 {
		t.Errorf("children after rebuild: a=%d b=%d, want 0/1",
			s.ZCard(childrenOf(a.ID)), s.ZCard(childrenOf(b.ID)))
	}
}

func TestMoveNodeEnforcesHierarchy(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)
	a, _ := r.Nodes.CreateCategory("A", "a", "")
	b, _ := r.Nodes.CreateCategory("B", "b", "")
	f, _ := r.Nodes.CreateForum(a.ID, "Forum", "forum", "", false)
	if err := r.Nodes.MoveNode(a.ID, b.ID); err == nil {
		t.Fatal("category nested below category")
	}
	if err := r.Nodes.MoveNode(f.ID, f.ID); err == nil {
		t.Fatal("forum nested below forum")
	}
	if err := r.Nodes.MoveNode(f.ID, b.ID); err != nil {
		t.Fatal(err)
	}
}

// TestThreadSortsAndViews covers the List sort keys and IncrementViews.
func TestThreadSortsAndViews(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)

	cat, _ := r.Nodes.CreateCategory("C", "c", "")
	f, _ := r.Nodes.CreateForum(cat.ID, "F", "f", "", false)

	t1, _, err := r.Threads.Create(f.ID, 1, "bir", "mesaj bir")
	if err != nil {
		t.Fatal(err)
	}
	t2, _, err := r.Threads.Create(f.ID, 1, "iki", "mesaj iki")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Posts.Create(t1.ID, 2, "yanıt"); err != nil {
		t.Fatal(err)
	}
	// Views: t2 gets 3 views, t1 one.
	for i := 0; i < 3; i++ {
		if err := r.Threads.IncrementViews(t2.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Threads.IncrementViews(t1.ID); err != nil {
		t.Fatal(err)
	}

	// "posts": t1 has 2 posts, t2 has 1 → t1 first.
	if ids := r.Threads.List(f.ID, "posts", 0, 10); len(ids) != 2 || ids[0] != t1.ID {
		t.Errorf("List(posts) = %v, want t1 first (%d)", ids, t1.ID)
	}
	// "views": t2 first.
	if ids := r.Threads.List(f.ID, "views", 0, 10); len(ids) != 2 || ids[0] != t2.ID {
		t.Errorf("List(views) = %v, want t2 first (%d)", ids, t2.ID)
	}
	// "new" is creation order (t2 created after t1 → newer first).
	if ids := r.Threads.List(f.ID, "new", 0, 10); len(ids) != 2 || ids[0] != t2.ID {
		t.Errorf("List(new) = %v, want t2 first", ids)
	}
	// Unknown sort → nil (no panic).
	if ids := r.Threads.List(f.ID, "bogus", 0, 10); ids != nil {
		t.Errorf("List(bogus) = %v, want nil", ids)
	}

	// Blob agrees with the index scores.
	th, _ := r.Threads.Get(t2.ID)
	if th.ViewCount != 3 {
		t.Errorf("t2 views = %d, want 3", th.ViewCount)
	}
	if sc, ok := s.ZScoreOf(nodeThreadsViewsIdx(f.ID), t2.ID); !ok || sc != 3 {
		t.Errorf("views index score t2 = %v (ok=%v), want 3", sc, ok)
	}
	// PostRepo.Get round-trip.
	p, ok := r.Posts.Get(th.FirstPostID)
	if !ok || p.ThreadID != t2.ID {
		t.Errorf("Posts.Get(%d) = %+v ok=%v", th.FirstPostID, p, ok)
	}
}
