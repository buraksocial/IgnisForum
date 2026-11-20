package repo

import (
	"fmt"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// NodeRepo manages the node tree. The whole hierarchy (categories + forums, with
// positions) is denormalised into the single forum_tree blob so the home page
// renders from ONE Get; per-node counts/last-post live in the node blob.
type NodeRepo struct {
	r *Repos
}

// CreateCategory adds a root category and (re)builds the tree blob.
func (n *NodeRepo) CreateCategory(title, slug, description string) (*domain.Node, error) {
	return n.create(domain.TypeCategory, 0, title, slug, description, nil)
}

// CreateForum adds a forum under parentID and (re)builds the tree blob.
func (n *NodeRepo) CreateForum(parentID uint64, title, slug, description string, allowPolls bool) (*domain.Node, error) {
	return n.create(domain.TypeForum, parentID, title, slug, description, func(o *domain.NodeOptions) { o.AllowPolls = allowPolls })
}

// ImportNode writes a source-ID-mapped node. It is restricted to the importer
// package's validated staging workflow and is intentionally replay-safe.
func (n *NodeRepo) ImportNode(id uint64, typ domain.NodeType, parentID uint64, title, slug, description string, position int) (*domain.Node, error) {
	if id == 0 || title == "" || position < 0 {
		return nil, fmt.Errorf("invalid imported node")
	}
	if typ == domain.TypeCategory && parentID != 0 {
		return nil, fmt.Errorf("imported category must be at root")
	}
	if typ == domain.TypeForum {
		parent, ok := n.Get(parentID)
		if !ok || parent.Type != domain.TypeCategory {
			return nil, fmt.Errorf("imported forum requires category parent")
		}
	} else if typ != domain.TypeCategory {
		return nil, fmt.Errorf("unsupported imported node type %q", typ)
	}
	if existing, ok := n.Get(id); ok && existing.Type != typ {
		return nil, fmt.Errorf("imported node id %d conflicts with existing node", id)
	}
	node := &domain.Node{ID: id, Type: typ, ParentID: parentID, Title: title,
		Slug: slug, Description: description, Position: position}
	n.r.S.MarkID(store.TypeNode, id)
	if err := n.r.S.PutJSON(keyNode(id), node); err != nil {
		return nil, err
	}
	n.r.S.ZAddIdx(childrenOf(parentID), float64(position), id)
	return node, nil
}

func (n *NodeRepo) create(typ domain.NodeType, parentID uint64, title, slug, description string, opts func(*domain.NodeOptions)) (*domain.Node, error) {
	s := n.r.S
	id := s.NextID()
	s.MarkID(store.TypeNode, id)
	node := &domain.Node{
		ID:          id,
		Title:       title,
		Slug:        slug,
		ParentID:    parentID,
		Type:        typ,
		Description: description,
		Position:    s.ZCard(childrenOf(parentID)),
		Options:     domain.NodeOptions{},
	}
	if opts != nil {
		opts(&node.Options)
	}

	// PRIMARY FIRST.
	if err := s.PutJSON(keyNode(id), node); err != nil {
		return nil, fmt.Errorf("put node: %w", err)
	}
	// INDEXES AFTER.
	s.ZAddIdx(childrenOf(parentID), float64(node.Position), id)
	if err := n.buildTree(); err != nil {
		return nil, fmt.Errorf("build tree: %w", err)
	}
	return node, nil
}

// Get returns a node by id (false on miss).
func (n *NodeRepo) Get(id uint64) (*domain.Node, bool) {
	var node domain.Node
	ok, _ := n.r.S.GetJSON(keyNode(id), &node)
	if !ok {
		return nil, false
	}
	return &node, true
}

// Tree is the deserialised forum_tree blob: categories with their ordered forums.
type Tree struct {
	Categories []TreeCat `json:"categories"`
}

// TreeCat is one category block with its forums.
type TreeCat struct {
	NodeID uint64      `json:"node_id"`
	Title  string      `json:"title"`
	Forums []TreeForum `json:"forums"`
}

// TreeForum is a row in the forum list.
type TreeForum struct {
	NodeID uint64 `json:"node_id"`
	Title  string `json:"title"`
	Slug   string `json:"slug"`
}

// LoadTree reads the home-page structure (the single Get that makes the index
// fast). Returns an empty tree if nothing is seeded yet.
func (n *NodeRepo) LoadTree() (*Tree, error) {
	var t Tree
	b, ok := n.r.S.GetRaw(store.KeyForumTree)
	if !ok || len(b) == 0 {
		return &Tree{}, nil
	}
	if err := n.r.S.DecodeJSON(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// BuildTree re-materialises the forum_tree blob from node primaries. Exported
// for the startup consistency pass: forum_tree is a PRIMARY blob so it survives
// restarts, but a crash between a node write and its tree update leaves it
// short; RebuildContent re-derives idx:children, then main calls this.
func (n *NodeRepo) BuildTree() error { return n.buildTree() }

// buildTree re-materialises forum_tree from the children index + node blobs.
// Called only on hierarchy changes (create/move), never on content writes.
func (n *NodeRepo) buildTree() error {
	s := n.r.S
	// Children are ordered by position ASCENDING → ZOldest (ZPage would be
	// highest-score/last-created first).
	cats := s.ZOldest(childrenOf(0), 0, 1<<30)
	tree := &Tree{}
	for _, cid := range cats {
		var c domain.Node
		if _, err := s.GetJSON(keyNode(cid), &c); err != nil || c.Type != domain.TypeCategory {
			continue
		}
		tc := TreeCat{NodeID: cid, Title: c.Title}
		for _, fid := range s.ZOldest(childrenOf(cid), 0, 1<<30) {
			var f domain.Node
			if _, err := s.GetJSON(keyNode(fid), &f); err != nil || f.Type != domain.TypeForum {
				continue
			}
			tc.Forums = append(tc.Forums, TreeForum{NodeID: fid, Title: f.Title, Slug: f.Slug})
		}
		tree.Categories = append(tree.Categories, tc)
	}
	return s.PutJSON(store.KeyForumTree, tree)
}

// MoveNode repositions a node under a new parent (admin drag & drop, M4). It
// removes the node from the OLD parent's children index before adding it to the
// new one — otherwise the forum appears under both categories in the tree.
func (n *NodeRepo) MoveNode(id, newParent uint64) error {
	s := n.r.S
	node, ok := n.Get(id)
	if !ok {
		return fmt.Errorf("node %d not found", id)
	}
	if node.Type == domain.TypeCategory && newParent != 0 {
		return fmt.Errorf("category %d must remain at the root", id)
	}
	if node.Type == domain.TypeForum {
		parent, exists := n.Get(newParent)
		if !exists || parent.Type != domain.TypeCategory {
			return fmt.Errorf("forum %d requires a category parent", id)
		}
	}
	if node.ParentID == newParent {
		return nil
	}
	oldParent := node.ParentID
	node.ParentID = newParent
	node.Position = s.ZCard(childrenOf(newParent))
	// primary first, indexes after.
	if err := s.PutJSON(keyNode(id), node); err != nil {
		return err
	}
	s.ZRem(childrenOf(oldParent), id)
	s.ZAddIdx(childrenOf(newParent), float64(node.Position), id)
	return n.buildTree()
}

// Update persists mutable presentation fields for a node. The type and
// hierarchy are intentionally handled separately by MoveNode.
func (n *NodeRepo) Update(id uint64, title, slug, description string, options domain.NodeOptions) error {
	node, ok := n.Get(id)
	if !ok {
		return fmt.Errorf("node %d not found", id)
	}
	node.Title, node.Slug, node.Description, node.Options = title, slug, description, options
	if err := n.r.S.PutJSON(keyNode(id), node); err != nil {
		return err
	}
	return n.buildTree()
}

// DeleteEmpty removes an empty node without cascading into content. Nodes with
// threads, posts, or children must first be explicitly migrated elsewhere.
func (n *NodeRepo) DeleteEmpty(id uint64) error {
	node, ok := n.Get(id)
	if !ok {
		return fmt.Errorf("node %d not found", id)
	}
	if n.r.S.ZCard(childrenOf(id)) != 0 || n.r.S.ZCard(NodeThreadsKey(id)) != 0 || node.ThreadCount != 0 || node.PostCount != 0 {
		return fmt.Errorf("node %d is not empty", id)
	}
	n.r.S.Delete(keyNode(id))
	n.r.S.ZRem(childrenOf(node.ParentID), id)
	return n.buildTree()
}

// RebuildContentCounts recomputes node counts and last-post summaries from
// imported primaries, avoiding counter drift when a staging import is resumed.
func (n *NodeRepo) RebuildContentCounts() error {
	for id := uint64(1); id <= n.r.S.MaxID(store.TypeNode); id++ {
		node, ok := n.Get(id)
		if !ok {
			continue
		}
		node.ThreadCount, node.PostCount, node.LastPost = 0, 0, nil
		if err := n.r.S.PutJSON(keyNode(id), node); err != nil {
			return err
		}
	}
	threadNodes := make(map[uint64]uint64)
	for id := uint64(1); id <= n.r.S.MaxID(store.TypeThread); id++ {
		thread, ok := n.r.Threads.Get(id)
		if !ok || thread.Status == domain.ThreadDeleted {
			continue
		}
		threadNodes[id] = thread.NodeID
		if node, exists := n.Get(thread.NodeID); exists {
			node.ThreadCount++
			if err := n.r.S.PutJSON(keyNode(node.ID), node); err != nil {
				return err
			}
		}
	}
	for id := uint64(1); id <= n.r.S.MaxID(store.TypePost); id++ {
		post, ok := n.r.Posts.Get(id)
		if !ok || post.State == 2 {
			continue
		}
		nodeID, ok := threadNodes[post.ThreadID]
		if !ok {
			continue
		}
		node, exists := n.Get(nodeID)
		if !exists {
			continue
		}
		node.PostCount++
		if node.LastPost == nil || post.Date.After(node.LastPost.T) {
			node.LastPost = &domain.LastPostRef{T: post.Date, PostID: post.ID, UserID: post.UserID}
		}
		if err := n.r.S.PutJSON(keyNode(nodeID), node); err != nil {
			return err
		}
	}
	return nil
}

// rebuildHook re-derives the children index for a node during RebuildContent.
// (The tree blob itself is rebuilt by the server after the walk completes.)
func (n *NodeRepo) rebuildHook(key string, blob []byte) {
	var node domain.Node
	if err := n.r.S.DecodeJSON(blob, &node); err != nil {
		return
	}
	n.r.S.ZAddIdx(childrenOf(node.ParentID), float64(node.Position), node.ID)
}
