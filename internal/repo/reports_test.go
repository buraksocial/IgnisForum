package repo

import "testing"

func TestReportsAreIdempotentResolveAndRebuild(t *testing.T) {
	s, _ := openTestStore(t)
	r := New(s)
	cat, err := r.Nodes.CreateCategory("General", "general", "")
	if err != nil {
		t.Fatal(err)
	}
	forum, err := r.Nodes.CreateForum(cat.ID, "Chat", "chat", "", false)
	if err != nil {
		t.Fatal(err)
	}
	thread, post, err := r.Threads.Create(forum.ID, 10, "Topic", "Post body")
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.CreateReport(post.ID, 11, "Spam or abuse")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := r.CreateReport(post.ID, 11, "Different reason")
	if err != nil || duplicate.ID != first.ID || duplicate.Reason != first.Reason {
		t.Fatalf("duplicate report = %+v %v", duplicate, err)
	}
	if got := r.OpenReports(0, 10); len(got) != 1 {
		t.Fatalf("open reports = %d", len(got))
	}
	if err := r.ResolveReport(first.ID, 99); err != nil {
		t.Fatal(err)
	}
	if got := r.OpenReports(0, 10); len(got) != 0 {
		t.Fatalf("resolved report still open: %d", len(got))
	}
	if _, err := r.CreateReport(post.ID, 11, "Reopened after a new incident"); err != nil {
		t.Fatal(err)
	}
	if got := r.OpenReports(0, 10); len(got) != 1 {
		t.Fatalf("reopened reports = %d", len(got))
	}
	s.RebuildContent(r.Hooks)
	if got := r.OpenReports(0, 10); len(got) != 1 || got[0].ThreadID != thread.ID {
		t.Fatalf("rebuilt reports = %+v", got)
	}
	if err := r.ResolveReport(first.ID, 99); err != nil {
		t.Fatal(err)
	}
	s.RebuildContent(r.Hooks)
	if got := r.OpenReports(0, 10); len(got) != 0 {
		t.Fatalf("resolved report resurrected after rebuild: %d", len(got))
	}
}
