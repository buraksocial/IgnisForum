package flarum

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/buraksocial/IgnisForum/internal/auth"
	"github.com/buraksocial/IgnisForum/internal/perm"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

func TestImportPreservesSourceIDsSanitizesAndReplays(t *testing.T) {
	s, err := store.Open(store.OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r := repo.New(s)
	a := auth.NewService(s)
	users := auth.NewUserRepo(a, auth.NewRateLimiter(s))
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("flarum-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	flarumHash := strings.Replace(string(passwordHash), "$2a$", "$2y$", 1)
	snapshot := testSnapshot(t, map[string][]map[string]any{
		"users":            {{"id": 5, "username": "legacyuser", "email": "legacy@example.test", "is_email_confirmed": 1, "password": flarumHash, "avatar_url": nil, "joined_at": "2023-01-01 00:00:00", "last_seen_at": nil, "suspended_until": nil, "suspend_reason": nil}},
		"groups":           {{"id": 1, "name_singular": "Admin", "name_plural": "Admins", "color": nil, "icon": nil, "is_hidden": 0, "created_at": nil}, {"id": 3, "name_singular": "Member", "name_plural": "Members", "color": nil, "icon": nil, "is_hidden": 0, "created_at": nil}},
		"group_user":       {{"user_id": 5, "group_id": 1, "created_at": nil}},
		"group_permission": {{"group_id": 3, "permission": "startDiscussion"}, {"group_id": 3, "permission": "discussion.reply"}, {"group_id": 3, "permission": "searchUsers"}},
		"discussions":      {{"id": 100, "title": "Imported topic", "comment_count": 1, "created_at": "2023-02-01 00:00:00", "user_id": 5, "first_post_id": 200, "last_posted_at": "2023-02-02 00:00:00", "last_posted_user_id": 5, "last_post_id": 201, "hidden_at": nil, "slug": "imported-topic", "is_private": 0, "is_approved": 1, "is_sticky": 0, "is_locked": 0}},
		"posts": {
			{"id": 200, "discussion_id": 100, "number": 1, "created_at": "2023-02-01 00:00:00", "user_id": 5, "type": "comment", "content": "<p>Hello <strong>world</strong><script>evil()</script></p>", "edited_at": nil, "edited_user_id": nil, "hidden_at": nil, "hidden_user_id": nil, "is_private": 0, "is_approved": 1},
			{"id": 201, "discussion_id": 100, "number": 2, "created_at": "2023-02-02 00:00:00", "user_id": 5, "type": "comment", "content": "<p>Reply</p>", "edited_at": nil, "edited_user_id": nil, "hidden_at": nil, "hidden_user_id": nil, "is_private": 0, "is_approved": 1},
		},
		"tags":           {{"id": 3, "name": "General", "slug": "general", "description": "General topics", "color": nil, "position": 0, "parent_id": nil, "is_restricted": 0, "is_hidden": 0, "created_at": nil}},
		"discussion_tag": {{"discussion_id": 100, "tag_id": 3, "created_at": nil}},
		"post_likes":     {{"post_id": 201, "user_id": 5, "created_at": nil}},
		"flags":          {}, "notifications": {},
	})
	digest := sha256.Sum256([]byte("fixture"))
	report, err := Import(snapshot, digest, s, r, users)
	if err != nil {
		t.Fatal(err)
	}
	if report.Tables["users"].Imported != 1 || report.Tables["discussions"].Imported != 1 || report.Tables["posts"].Imported != 2 || report.Tables["post_likes"].Imported != 1 || report.Tables["group_permission"].Imported != 2 {
		t.Fatalf("unexpected import counts: %+v", report.Tables)
	}
	migratedPerms := perm.NewEngine(s)
	if !migratedPerms.Effective(6, []uint64{perm.GroupMember}, perm.ScopeGlobal, 0, perm.KeyForumThread) || !migratedPerms.Effective(6, []uint64{perm.GroupMember}, perm.ScopeGlobal, 0, perm.KeyForumReply) {
		t.Fatal("mapped Flarum member permissions were not applied")
	}
	if _, ok := users.GetByID(5); !ok {
		t.Fatal("source user ID was not preserved")
	}
	thread, ok := r.Threads.Get(100)
	if !ok || thread.FirstPostID != 200 || thread.NodeID != 3 || len(thread.TagIDs) != 1 || thread.TagIDs[0] != 3 {
		t.Fatalf("imported thread = %+v, found=%v", thread, ok)
	}
	first, ok := r.Posts.Get(200)
	if !ok || first.MessageRaw == "" || !strings.Contains(first.MessageRendered, "<strong>world</strong>") || strings.Contains(first.MessageRendered, "script") {
		t.Fatalf("imported post HTML not safely preserved: %+v", first)
	}
	forum, ok := r.Nodes.Get(3)
	if !ok || forum.ThreadCount != 1 || forum.PostCount != 2 {
		t.Fatalf("reconciled forum counters = %+v", forum)
	}
	if kind, ok := r.Reactions.Get(201, 5); !ok || kind != "like" {
		t.Fatalf("imported reaction = %q, %v", kind, ok)
	}
	if _, _, err := users.Login("legacyuser", "flarum-password-123", false, "127.0.0.1"); err != nil {
		t.Fatalf("migrated bcrypt login: %v", err)
	}
	upgraded, _ := users.GetByID(5)
	if !strings.HasPrefix(upgraded.PasswordHash, "$argon2id$") {
		t.Fatal("migrated password was not upgraded on login")
	}

	if _, err := Import(snapshot, digest, s, r, users); err != nil {
		t.Fatalf("same snapshot replay: %v", err)
	}
	upgraded, _ = users.GetByID(5)
	if !strings.HasPrefix(upgraded.PasswordHash, "$argon2id$") {
		t.Fatal("same-snapshot replay downgraded the upgraded password hash")
	}
	if s.ZCard(repo.NodeThreadsKey(3)) != 1 || s.ZCard(repo.ThreadPostsKey(100)) != 2 {
		t.Fatal("replay duplicated or lost derived index members")
	}
	otherDigest := sha256.Sum256([]byte("different snapshot"))
	if _, err := Import(snapshot, otherDigest, s, r, users); err == nil {
		t.Fatal("different snapshot was accepted into an initialized staging store")
	}
}

func testSnapshot(t *testing.T, rows map[string][]map[string]any) *Snapshot {
	t.Helper()
	tables := make(map[string]json.RawMessage, len(rows))
	for name, values := range rows {
		encodedRows := make([]json.RawMessage, 0, len(values))
		for _, row := range values {
			b, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			encodedRows = append(encodedRows, b)
		}
		b, err := json.Marshal(tableEnvelope{SourceCount: len(values), Rows: encodedRows})
		if err != nil {
			t.Fatal(err)
		}
		tables[name] = b
	}
	snapshot := &Snapshot{FormatVersion: SnapshotFormatVersion, Source: "flarum", Tables: tables}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
