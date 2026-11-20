package repo

import (
	"errors"

	"github.com/buraksocial/IgnisForum/internal/domain"
)

var errInvalidThreadUserState = errors.New("invalid thread user state")

// GetThreadUserState returns a member's durable read/subscription state.
func (r *Repos) GetThreadUserState(threadID, userID uint64) (*domain.ThreadUserState, bool) {
	var state domain.ThreadUserState
	ok, err := r.S.GetJSON(keyThreadUserState(threadID, userID), &state)
	if err != nil || !ok {
		return nil, false
	}
	return &state, true
}

// PutThreadUserState persists the primary per-member thread state.
func (r *Repos) PutThreadUserState(state *domain.ThreadUserState) error {
	if state == nil || state.ThreadID == 0 || state.UserID == 0 || (state.Subscription != "" && state.Subscription != "follow" && state.Subscription != "ignore") {
		return errInvalidThreadUserState
	}
	if _, ok := r.Threads.Get(state.ThreadID); !ok {
		return errInvalidThreadUserState
	}
	return r.S.PutJSON(keyThreadUserState(state.ThreadID, state.UserID), state)
}
