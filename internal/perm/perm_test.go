package perm

import (
	"testing"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(store.OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedNode writes a minimal node blob so the inheritance walk can traverse it.
func seedNode(t *testing.T, s *store.Store, id, parent uint64) {
	t.Helper()
	n := domain.Node{ID: id, ParentID: parent, Type: domain.TypeForum, Title: "F"}
	if err := s.PutJSON("node:"+itoa(id), &n); err != nil {
		t.Fatal(err)
	}
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// TestBoundaryEveryKey is the M2 acceptance (§11): for EVERY permission key in
// the matrix, prove at least one allow and one deny through the real engine —
// guest (uid 0) and member paths at global scope, then per-node override.
func TestBoundaryEveryKey(t *testing.T) {
	s := openStore(t)
	e := NewEngine(s)
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	member := []uint64{GroupMember}
	admin := []uint64{GroupAdmin}

	// Global scope: every key has a definite answer for guest and member.
	wantGuest := map[string]bool{
		KeyAccess: true, KeyForumView: true, KeySearch: true,
		KeyUserRegister: true, // the register gate lives on Guest
		KeyForumThread:  false, KeyForumReply: false, KeyAdmin: false,
	}
	wantMember := map[string]bool{
		KeyAccess: true, KeyForumView: true, KeySearch: true,
		KeyForumThread: true, KeyForumReply: true,
		KeyUserRegister: true, KeyAdmin: false,
	}

	// Allow-cases: the stock matrix already allows the member everywhere but
	// admin; deny-cases: guest posting + anyone's admin via absence.
	for _, k := range AllKeys {
		if got := e.Effective(0, nil, ScopeGlobal, 0, k); got != wantGuest[k] {
			t.Errorf("guest %s = %v, want %v", k, got, wantGuest[k])
		}
		if got := e.Effective(2, member, ScopeGlobal, 0, k); got != wantMember[k] {
			t.Errorf("member %s = %v, want %v", k, got, wantMember[k])
		}
	}

	// Node scope walks parents: node 10 (parent 5, parent 0/global).
	seedNode(t, s, 5, 0)
	seedNode(t, s, 10, 5)

	// Member inherits the global allow down the chain.
	if !e.Effective(2, member, ScopeNode, 10, KeyForumReply) {
		t.Error("member must inherit reply down the parent chain")
	}
	// Explicit deny at the child node overrides the inherited allow.
	if err := e.SetGrant(GroupMember, ScopeNode, 10, map[string]bool{KeyForumReply: false}); err != nil {
		t.Fatal(err)
	}
	if e.Effective(2, member, ScopeNode, 10, KeyForumReply) {
		t.Error("node deny must override inherited global allow")
	}
	// Sibling node keeps the allow (the deny was node-scoped).
	seedNode(t, s, 11, 5)
	if !e.Effective(2, member, ScopeNode, 11, KeyForumReply) {
		t.Error("deny on node 10 must not leak to sibling 11")
	}
	// Explicit allow at node scope overrides a guest global deny.
	if err := e.SetGrant(GroupGuest, ScopeNode, 11, map[string]bool{KeyForumReply: true}); err != nil {
		t.Fatal(err)
	}
	if !e.Effective(0, nil, ScopeNode, 11, KeyForumReply) {
		t.Error("node allow must override global guest deny")
	}
	// Admin group allows everything everywhere.
	for _, k := range AllKeys {
		if !e.Effective(1, admin, ScopeGlobal, 0, k) {
			t.Errorf("admin %s denied", k)
		}
	}
}

func TestCustomGroupLifecycleAndPermissionResolution(t *testing.T) {
	s := openStore(t)
	e := NewEngine(s)
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	group, err := e.CreateGroup("Moderators", "Can review community content")
	if err != nil {
		t.Fatal(err)
	}
	if group.System || group.ID <= GroupAdmin {
		t.Fatalf("custom group = %+v", group)
	}
	if err := e.SetGrant(group.ID, ScopeGlobal, 0, map[string]bool{KeyForumReply: true}); err != nil {
		t.Fatal(err)
	}
	if !e.Effective(42, []uint64{group.ID}, ScopeGlobal, 0, KeyForumReply) {
		t.Fatal("custom group grant was not resolved")
	}
	if err := e.UpdateGroup(group.ID, "Trusted moderators", "updated"); err != nil {
		t.Fatal(err)
	}
	if got, ok := e.GetGroup(group.ID); !ok || got.Name != "Trusted moderators" {
		t.Fatalf("updated group = %+v, ok=%v", got, ok)
	}
	if err := e.DeleteGroup(GroupAdmin); err != ErrSystemGroup {
		t.Fatalf("system group delete = %v", err)
	}
	if err := e.DeleteGroup(group.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.GetGroup(group.ID); ok {
		t.Fatal("deleted custom group still exists")
	}
}

// TestMultiGroupMerge: user in Member + explicit group granting reply where
// the stock member lacks it (deny at member level) → OR merge allows.
func TestMultiGroupMerge(t *testing.T) {
	s := openStore(t)
	e := NewEngine(s)
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// A custom group 7 that explicitly allows admin (moderator+ style).
	if err := e.SetGrant(7, ScopeGlobal, 0, map[string]bool{KeyAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if !e.Effective(3, []uint64{GroupMember, 7}, ScopeGlobal, 0, KeyAdmin) {
		t.Error("group OR-merge must allow admin through group 7")
	}
	if e.Effective(3, []uint64{GroupMember}, ScopeGlobal, 0, KeyAdmin) {
		t.Error("plain member must not get admin")
	}
}

// TestDeleteGrantRestoresInheritance: deleting the node override makes the
// global answer effective again.
func TestDeleteGrantRestoresInheritance(t *testing.T) {
	s := openStore(t)
	e := NewEngine(s)
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	seedNode(t, s, 4, 0)
	if err := e.SetGrant(GroupMember, ScopeNode, 4, map[string]bool{KeyForumView: false}); err != nil {
		t.Fatal(err)
	}
	if e.Effective(2, []uint64{GroupMember}, ScopeNode, 4, KeyForumView) {
		t.Fatal("override must deny before deletion")
	}
	if err := e.SetGrant(GroupMember, ScopeNode, 4, nil); err != nil {
		t.Fatal(err)
	}
	if !e.Effective(2, []uint64{GroupMember}, ScopeNode, 4, KeyForumView) {
		t.Error("after delete the global allow must be effective again")
	}
}

// TestCacheInvalidationOnWrite proves bump-on-write: answering caches the
// result, a subsequent SetGrant forces re-derivation (no stale allow).
func TestCacheInvalidationOnWrite(t *testing.T) {
	s := openStore(t)
	e := NewEngine(s)
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Warm the cache.
	if !e.Effective(2, []uint64{GroupMember}, ScopeGlobal, 0, KeyForumReply) {
		t.Fatal("precondition: member may reply")
	}
	// Write a global member deny — cache must invalidate.
	if err := e.SetGrant(GroupMember, ScopeGlobal, 0, map[string]bool{KeyForumReply: false}); err != nil {
		t.Fatal(err)
	}
	if e.Effective(2, []uint64{GroupMember}, ScopeGlobal, 0, KeyForumReply) {
		t.Error("stale cache: deny after write must be visible immediately")
	}
}
