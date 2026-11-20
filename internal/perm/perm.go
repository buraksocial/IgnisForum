// Package perm is the XenForo-style permission engine: grants live as KV
// blobs under perm:{group}:{ctype}:{cid}, resolution merges the caller's
// groups (OR), and node-scoped grants inherit up the parent chain until a
// grant (or the global default) answers the key. An in-memory perm cache is
// the sanctioned read cache (§ plan: bump-on-write, lazy invalidate) because
// permission checks run on every listing — it holds derived answers, never
// primary data, and any grant write clears it.
package perm

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// Permission keys (M2 set — the matrix the acceptance test walks).
const (
	KeyAccess       = "access"        // may use the site at all
	KeyForumView    = "forum_view"    // may see the forum/thread
	KeyForumThread  = "forum_thread"  // may start threads
	KeyForumReply   = "forum_reply"   // may reply
	KeySearch       = "search"        // may run search
	KeyUserRegister = "user_register" // may register
	KeyAdmin        = "admin"         // admin cp
)

var ErrSystemGroup = errors.New("perm: system group cannot be changed")

func groupKey(id uint64) string { return fmt.Sprintf("group:%d", id) }

// ListGroups enumerates the bounded group high-water mark, skipping deleted
// records because the store intentionally has no key-scan primitive.
func (e *Engine) ListGroups() []*domain.Group {
	max := e.s.MaxID(store.TypeGroup)
	groups := make([]*domain.Group, 0, 8)
	for id := uint64(1); id <= max; id++ {
		var group domain.Group
		if ok, _ := e.s.GetJSON(groupKey(id), &group); ok {
			groups = append(groups, &group)
		}
	}
	return groups
}

func (e *Engine) GetGroup(id uint64) (*domain.Group, bool) {
	var group domain.Group
	if ok, _ := e.s.GetJSON(groupKey(id), &group); !ok {
		return nil, false
	}
	return &group, true
}

func (e *Engine) CreateGroup(name, description string) (*domain.Group, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if len(name) < 2 || len(name) > 80 || len(description) > 500 {
		return nil, errors.New("perm: invalid group fields")
	}
	id := e.s.NextID()
	e.s.MarkID(store.TypeGroup, id)
	group := &domain.Group{ID: id, Name: name, Description: description}
	if err := e.s.PutJSON(groupKey(id), group); err != nil {
		return nil, err
	}
	return group, nil
}

func (e *Engine) UpdateGroup(id uint64, name, description string) error {
	group, ok := e.GetGroup(id)
	if !ok {
		return errors.New("perm: group not found")
	}
	if group.System {
		return ErrSystemGroup
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if len(name) < 2 || len(name) > 80 || len(description) > 500 {
		return errors.New("perm: invalid group fields")
	}
	group.Name, group.Description = name, description
	return e.s.PutJSON(groupKey(id), group)
}

func (e *Engine) DeleteGroup(id uint64) error {
	group, ok := e.GetGroup(id)
	if !ok {
		return errors.New("perm: group not found")
	}
	if group.System {
		return ErrSystemGroup
	}
	e.s.Delete(groupKey(id))
	// Permission scopes are global or node-scoped. Remove both kinds of
	// derived grant records while walking the bounded node high-water mark.
	e.s.Delete(keyGrant(id, ScopeGlobal, 0))
	for nodeID := uint64(1); nodeID <= e.s.MaxID(store.TypeNode); nodeID++ {
		e.s.Delete(keyGrant(id, ScopeNode, nodeID))
	}
	e.invalidate()
	return nil
}

// AllKeys is the full matrix (order = stable for tests/admin UI).
var AllKeys = []string{
	KeyAccess, KeyForumView, KeyForumThread, KeyForumReply, KeySearch,
	KeyUserRegister, KeyAdmin,
}

// Group ids fixed at bootstrap (M2 keeps it simple; M4 makes them editable).
const (
	GroupGuest  uint64 = 1
	GroupMember uint64 = 2
	GroupAdmin  uint64 = 3
)

// Scopes.
const (
	ScopeGlobal = "*"    // global default grant, cid=0
	ScopeNode   = "node" // per-node override, cid=nodeID
)

// Grant is a permission override record: key -> explicitly allow/deny.
type Grant struct {
	Perms map[string]bool `json:"perms"`
}

// Engine resolves effective permissions over the store.
type Engine struct {
	s *store.Store

	// effective-permission cache. Grants are rare admin writes; every write
	// bumps the version and the next reads re-derive. (No TTL needed: the
	// bump-on-write makes staleness impossible within this process.)
	mu    sync.RWMutex
	cache map[string]bool
}

// NewEngine builds the permission engine.
func NewEngine(s *store.Store) *Engine {
	return &Engine{s: s, cache: make(map[string]bool)}
}

func keyGrant(group uint64, ctype string, cid uint64) string {
	return fmt.Sprintf("perm:%d:%s:%d", group, ctype, cid)
}

// SetGrant writes an explicit permission override for (group, scope) and
// invalidates the cache. Pass nil map to delete the grant (inherit again).
func (e *Engine) SetGrant(group uint64, ctype string, cid uint64, perms map[string]bool) error {
	if perms == nil {
		e.s.Delete(keyGrant(group, ctype, cid))
		e.invalidate()
		return nil
	}
	if err := e.s.PutJSON(keyGrant(group, ctype, cid), &Grant{Perms: perms}); err != nil {
		return err
	}
	e.invalidate()
	return nil
}

// GetGrant reads a raw grant (nil if unset).
func (e *Engine) GetGrant(group uint64, ctype string, cid uint64) map[string]bool {
	var g Grant
	if ok, _ := e.s.GetJSON(keyGrant(group, ctype, cid), &g); !ok {
		return nil
	}
	return g.Perms
}

func (e *Engine) invalidate() {
	e.mu.Lock()
	e.cache = make(map[string]bool)
	e.mu.Unlock()
}

// Invalidate clears the cache (call after node moves: the parent chain the
// answers came from changed).
func (e *Engine) Invalidate() { e.invalidate() }

// groupList normalises the caller's groups. Guest (uid 0 / no groups) checks
// against the Guest group so "who is allowed to see" is uniform.
func groupList(userID uint64, groups []uint64) []uint64 {
	if userID == 0 || len(groups) == 0 {
		return []uint64{GroupGuest}
	}
	return groups
}

// Effective answers key for the user over (ctype, cid), following node
// inheritance. The nearest scope that contains an explicit answer for the key
// wins (XenForo content-permission semantics); group grants at the same scope
// merge with OR so the most permissive group decides.
func (e *Engine) Effective(userID uint64, groups []uint64, ctype string, cid uint64, key string) bool {
	sig := cacheSig(userID, groups, ctype, cid, key)
	e.mu.RLock()
	v, ok := e.cache[sig]
	e.mu.RUnlock()
	if ok {
		return v
	}
	res := e.resolve(groupList(userID, groups), ctype, cid, key)
	e.mu.Lock()
	e.cache[sig] = res
	e.mu.Unlock()
	return res
}

// resolve walks scope→parents answering key from the group grants.
func (e *Engine) resolve(groups []uint64, ctype string, cid uint64, key string) bool {
	if ctype == ScopeNode {
		// Walk the node chain upward until a grant set at this level answers.
		for nodeID := cid; nodeID != 0; {
			var node domain.Node
			if ok, _ := e.s.GetJSON(fmt.Sprintf("node:%d", nodeID), &node); !ok {
				break
			}
			answered, allowed := e.atScope(groups, ScopeNode, nodeID, key)
			if answered {
				return allowed
			}
			nodeID = node.ParentID
		}
	}
	// Global default (and the fallthrough for scope="*").
	if answered, allowed := e.atScope(groups, ScopeGlobal, 0, key); answered {
		return allowed
	}
	return false // default deny
}

// atScope ORs the group grants at one scope; answered = at least one group
// has an explicit entry for the key.
func (e *Engine) atScope(groups []uint64, ctype string, cid uint64, key string) (answered, allowed bool) {
	for _, g := range groups {
		perms := e.GetGrant(g, ctype, cid)
		if perms == nil {
			continue
		}
		if v, has := perms[key]; has {
			answered = true
			if v {
				return true, true
			}
		}
	}
	return answered, false
}

func cacheSig(userID uint64, groups []uint64, ctype string, cid uint64, key string) string {
	var sb strings.Builder
	sb.Grow(64)
	sb.WriteByte('u')
	fmt.Fprintf(&sb, "%d|", userID)
	for _, g := range groups {
		fmt.Fprintf(&sb, "%d,", g)
	}
	fmt.Fprintf(&sb, "|%s:%d|%s", ctype, cid, key)
	return sb.String()
}

// Bootstrap writes the stock matrix:
//   - Guest:   access + forum_view + search allow; post/reply/register deny.
//   - Member:  everything except admin allow.
//   - Admin:   everything allow.
//
// Idempotent: skips when the member global grant already exists.
func (e *Engine) Bootstrap() error {
	// The built-in group IDs are reserved in the shared allocator so the first
	// generated user/node/etc. cannot collide with a group record.
	e.s.AdvanceIDFloor(GroupAdmin)
	for _, group := range []*domain.Group{
		{ID: GroupGuest, Name: "Guest", System: true},
		{ID: GroupMember, Name: "Member", System: true},
		{ID: GroupAdmin, Name: "Administrator", System: true},
	} {
		if _, ok := e.GetGroup(group.ID); !ok {
			if err := e.s.PutJSON(groupKey(group.ID), group); err != nil {
				return err
			}
			e.s.MarkID(store.TypeGroup, group.ID)
		}
	}
	if e.GetGrant(GroupMember, ScopeGlobal, 0) != nil {
		return nil
	}
	guest := map[string]bool{}
	member := map[string]bool{}
	admin := map[string]bool{}
	for _, k := range AllKeys {
		switch k {
		case KeyAccess, KeyForumView, KeySearch, KeyUserRegister:
			// The register grant lives on the Guest group (XenForo semantics):
			// "registration closed" = flip this one bit.
			guest[k] = true
		case KeyForumThread, KeyForumReply, KeyAdmin:
			guest[k] = false
		}
		member[k] = k != KeyAdmin
		admin[k] = true
	}
	if err := e.SetGrant(GroupGuest, ScopeGlobal, 0, guest); err != nil {
		return err
	}
	if err := e.SetGrant(GroupMember, ScopeGlobal, 0, member); err != nil {
		return err
	}
	if err := e.SetGrant(GroupAdmin, ScopeGlobal, 0, admin); err != nil {
		return err
	}
	e.invalidate()
	return nil
}
