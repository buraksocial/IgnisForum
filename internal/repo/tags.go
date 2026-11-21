package repo

import (
	"fmt"
	"strings"

	"github.com/buraksocial/IgnisForum/internal/bbcode"
	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

type TagRepo struct{ r *Repos }

func tagKey(id uint64) string       { return fmt.Sprintf("tag:%d", id) }
func tagSlugKey(slug string) string { return "tag_slug:" + slug }

// ApplyNames normalizes and deduplicates user-entered labels, creates canonical
// tag records, then writes their IDs into the thread and derived tag indexes.
func (t *TagRepo) ApplyNames(threadID uint64, names []string) error {
	thread, ok := t.r.Threads.Get(threadID)
	if !ok {
		return fmt.Errorf("thread %d not found", threadID)
	}
	ids := make([]uint64, 0, len(names))
	seen := make(map[uint64]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if len([]rune(name)) > 32 {
			return fmt.Errorf("tag exceeds 32 characters")
		}
		slug := bbcode.Slug(strings.ToLower(name))
		if slug == "" {
			return fmt.Errorf("invalid tag")
		}
		var id uint64
		if found, _ := t.r.S.GetJSON(tagSlugKey(slug), &id); !found || id == 0 {
			id = t.r.S.NextID()
			t.r.S.MarkID(store.TypeTag, id)
			tag := &domain.Tag{ID: id, Slug: slug, Title: name}
			if err := t.r.S.PutJSON(tagKey(id), tag); err != nil {
				return err
			}
			if err := t.r.S.PutJSON(tagSlugKey(slug), id); err != nil {
				return err
			}
			t.r.S.ZAddIdx(IdxTagSlug, float64(id), id)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, oldID := range thread.TagIDs {
		t.r.S.ZRem(tagThreadsIdx(oldID), threadID)
	}
	thread.TagIDs = ids
	thread.Version++
	if err := t.r.S.PutJSON(keyThread(threadID), thread); err != nil {
		return err
	}
	if thread.Status != domain.ThreadDeleted && thread.DiscussionState == domain.DiscussionVisible {
		for _, id := range ids {
			t.r.S.ZAddIdx(tagThreadsIdx(id), ts(thread.Date), threadID)
		}
	}
	return nil
}

func (t *TagRepo) All() []*domain.Tag {
	max := t.r.S.MaxID(store.TypeTag)
	out := make([]*domain.Tag, 0, max)
	for id := uint64(1); id <= max; id++ {
		if tag, ok := t.ByID(id); ok {
			out = append(out, tag)
		}
	}
	return out
}

// SetIDs replaces a thread's tags with existing tag records only.
func (t *TagRepo) SetIDs(threadID uint64, ids []uint64) error {
	thread, ok := t.r.Threads.Get(threadID)
	if !ok {
		return fmt.Errorf("thread %d not found", threadID)
	}
	unique := make([]uint64, 0, len(ids))
	seen := make(map[uint64]bool)
	for _, id := range ids {
		if _, ok := t.ByID(id); !ok {
			return fmt.Errorf("tag %d not found", id)
		}
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	for _, id := range thread.TagIDs {
		t.r.S.ZRem(tagThreadsIdx(id), threadID)
	}
	thread.TagIDs = unique
	thread.Version++
	if err := t.r.S.PutJSON(keyThread(threadID), thread); err != nil {
		return err
	}
	if thread.Status != domain.ThreadDeleted && thread.DiscussionState == domain.DiscussionVisible {
		for _, id := range unique {
			t.r.S.ZAddIdx(tagThreadsIdx(id), ts(thread.Date), threadID)
		}
	}
	return nil
}

func (t *TagRepo) BySlug(slug string) (*domain.Tag, bool) {
	var id uint64
	if ok, _ := t.r.S.GetJSON(tagSlugKey(slug), &id); !ok || id == 0 {
		return nil, false
	}
	var tag domain.Tag
	if ok, _ := t.r.S.GetJSON(tagKey(id), &tag); !ok {
		return nil, false
	}
	tag.ThreadCount = t.r.S.ZCard(tagThreadsIdx(id))
	return &tag, true
}

func (t *TagRepo) ByID(id uint64) (*domain.Tag, bool) {
	var tag domain.Tag
	if ok, _ := t.r.S.GetJSON(tagKey(id), &tag); !ok {
		return nil, false
	}
	tag.ThreadCount = t.r.S.ZCard(tagThreadsIdx(id))
	return &tag, true
}

func (t *TagRepo) List(slug string, offset, n int) []uint64 {
	tag, ok := t.BySlug(slug)
	if !ok {
		return nil
	}
	return t.r.S.ZPage(tagThreadsIdx(tag.ID), offset, n)
}

func (t *TagRepo) rebuildHook(_ string, blob []byte) {
	var tag domain.Tag
	if err := t.r.S.DecodeJSON(blob, &tag); err != nil || tag.ID == 0 || tag.Slug == "" {
		return
	}
	_ = t.r.S.PutJSON(tagSlugKey(tag.Slug), tag.ID)
	t.r.S.ZAddIdx(IdxTagSlug, float64(tag.ID), tag.ID)
	tag.ThreadCount = t.r.S.ZCard(tagThreadsIdx(tag.ID))
	_ = t.r.S.PutJSON(tagKey(tag.ID), &tag)
}
