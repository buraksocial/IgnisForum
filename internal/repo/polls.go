package repo

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

type PollRepo struct {
	r  *Repos
	mu sync.Mutex
}

func pollKey(threadID uint64) string { return fmt.Sprintf("poll:%d", threadID) }
func pollVoteKey(id uint64) string   { return fmt.Sprintf("poll_vote:%d", id) }
func pollVoterKey(threadID, userID uint64) string {
	return fmt.Sprintf("poll_voter:%d:%d", threadID, userID)
}

func (p *PollRepo) Create(threadID uint64, question string, options []string, multiple bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	thread, ok := p.r.Threads.Get(threadID)
	if !ok {
		return fmt.Errorf("thread not found")
	}
	node, ok := p.r.Nodes.Get(thread.NodeID)
	if !ok || !node.Options.AllowPolls {
		return fmt.Errorf("polls are disabled in this forum")
	}
	if thread.PollID != 0 {
		return fmt.Errorf("thread already has a poll")
	}
	question = strings.TrimSpace(question)
	if utf8.RuneCountInString(question) < 3 || utf8.RuneCountInString(question) > 150 || len(options) < 2 || len(options) > 10 {
		return fmt.Errorf("invalid poll question or option count")
	}
	poll := &domain.Poll{ThreadID: threadID, Question: question, Multiple: multiple}
	seen := make(map[string]bool)
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" || utf8.RuneCountInString(option) > 100 {
			return fmt.Errorf("poll options must be 1-100 characters")
		}
		if seen[strings.ToLower(option)] {
			return fmt.Errorf("poll options must be unique")
		}
		seen[strings.ToLower(option)] = true
		poll.Options = append(poll.Options, domain.PollOption{Text: option})
	}
	if err := p.r.S.PutJSON(pollKey(threadID), poll); err != nil {
		return err
	}
	thread.PollID = threadID
	thread.Version++
	return p.r.S.PutJSON(keyThread(threadID), thread)
}

func (p *PollRepo) Get(threadID uint64) (*domain.Poll, bool) {
	var poll domain.Poll
	if ok, _ := p.r.S.GetJSON(pollKey(threadID), &poll); !ok {
		return nil, false
	}
	return &poll, true
}

func (p *PollRepo) Vote(threadID, userID uint64, choices []int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if userID == 0 {
		return fmt.Errorf("authentication required")
	}
	thread, ok := p.r.Threads.Get(threadID)
	if !ok || thread.PollID != threadID || thread.Status != domain.ThreadOpen || thread.DiscussionState != domain.DiscussionVisible {
		return fmt.Errorf("poll unavailable")
	}
	node, ok := p.r.Nodes.Get(thread.NodeID)
	if !ok || !node.Options.AllowPolls {
		return fmt.Errorf("polls disabled")
	}
	poll, ok := p.Get(threadID)
	if !ok {
		return fmt.Errorf("poll not found")
	}
	if len(choices) == 0 || (!poll.Multiple && len(choices) != 1) {
		return fmt.Errorf("invalid selection")
	}
	seen := make(map[int]bool)
	for _, choice := range choices {
		if choice < 0 || choice >= len(poll.Options) || seen[choice] {
			return fmt.Errorf("invalid option")
		}
		seen[choice] = true
	}
	if _, exists := p.r.S.GetRaw(pollVoterKey(threadID, userID)); exists {
		return fmt.Errorf("already voted")
	}
	id := p.r.S.NextID()
	p.r.S.MarkID(store.TypePollVote, id)
	vote := &domain.PollVote{ID: id, ThreadID: threadID, UserID: userID, Options: append([]int(nil), choices...)}
	if err := p.r.S.PutJSON(pollVoteKey(id), vote); err != nil {
		return err
	}
	if err := p.r.S.PutJSON(pollVoterKey(threadID, userID), id); err != nil {
		return err
	}
	for _, choice := range choices {
		poll.Options[choice].Votes++
	}
	return p.r.S.PutJSON(pollKey(threadID), poll)
}

func (p *PollRepo) UserVote(threadID, userID uint64) ([]int, bool) {
	var id uint64
	if ok, _ := p.r.S.GetJSON(pollVoterKey(threadID, userID), &id); !ok || id == 0 {
		return nil, false
	}
	var vote domain.PollVote
	if ok, _ := p.r.S.GetJSON(pollVoteKey(id), &vote); !ok {
		return nil, false
	}
	return vote.Options, true
}

// rebuildCounts resets counters from the authoritative vote records; vote hooks
// replay afterward and restore them without scanning voters on page requests.
func (p *PollRepo) rebuildCounts(thread *domain.Thread) {
	if thread.PollID == 0 {
		return
	}
	poll, ok := p.Get(thread.ID)
	if !ok {
		return
	}
	for i := range poll.Options {
		poll.Options[i].Votes = 0
	}
	_ = p.r.S.PutJSON(pollKey(thread.ID), poll)
}

func (p *PollRepo) rebuildVoteHook(_ string, blob []byte) {
	var vote domain.PollVote
	if err := p.r.S.DecodeJSON(blob, &vote); err != nil || vote.ID == 0 || vote.ThreadID == 0 || vote.UserID == 0 {
		return
	}
	poll, ok := p.Get(vote.ThreadID)
	if !ok {
		return
	}
	thread, exists := p.r.Threads.Get(vote.ThreadID)
	if !exists || thread.Status == domain.ThreadDeleted {
		return
	}
	for _, choice := range vote.Options {
		if choice >= 0 && choice < len(poll.Options) {
			poll.Options[choice].Votes++
		}
	}
	_ = p.r.S.PutJSON(pollKey(vote.ThreadID), poll)
	_ = p.r.S.PutJSON(pollVoterKey(vote.ThreadID, vote.UserID), vote.ID)
}
