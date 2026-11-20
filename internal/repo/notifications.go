package repo

import (
	"fmt"
	"time"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

type NotificationRepo struct{ r *Repos }

func (n *NotificationRepo) Create(recipientID, actorID, threadID, postID uint64) error {
	if recipientID == 0 || actorID == recipientID || threadID == 0 || postID == 0 {
		return nil
	}
	id := n.r.S.NextID()
	n.r.S.MarkID(store.TypeNotification, id)
	item := &domain.Notification{ID: id, UserID: recipientID, ActorID: actorID, ThreadID: threadID, PostID: postID, CreatedAt: time.Now().UTC()}
	if err := n.r.S.PutJSON(keyNotification(id), item); err != nil {
		return err
	}
	n.index(item)
	return nil
}

func (n *NotificationRepo) Get(id uint64) (*domain.Notification, bool) {
	var item domain.Notification
	if ok, _ := n.r.S.GetJSON(keyNotification(id), &item); !ok {
		return nil, false
	}
	return &item, true
}

func (n *NotificationRepo) List(userID uint64, offset, count int) []uint64 {
	return n.r.S.ZPage(userNotificationsIdx(userID), offset, count)
}

func (n *NotificationRepo) UnreadCount(userID uint64) int {
	return n.r.S.ZCard(userUnreadNotificationsIdx(userID))
}

func (n *NotificationRepo) MarkRead(userID, id uint64) error {
	item, ok := n.Get(id)
	if !ok || item.UserID != userID {
		return fmt.Errorf("notification not found")
	}
	if item.Read {
		return nil
	}
	item.Read = true
	if err := n.r.S.PutJSON(keyNotification(id), item); err != nil {
		return err
	}
	n.r.S.ZRem(userUnreadNotificationsIdx(userID), id)
	return nil
}

func (n *NotificationRepo) index(item *domain.Notification) {
	n.r.S.ZAddIdx(userNotificationsIdx(item.UserID), float64(item.CreatedAt.UnixNano()), item.ID)
	if !item.Read {
		n.r.S.ZAddIdx(userUnreadNotificationsIdx(item.UserID), float64(item.CreatedAt.UnixNano()), item.ID)
	}
}

func (n *NotificationRepo) rebuildHook(_ string, blob []byte) {
	var item domain.Notification
	if err := n.r.S.DecodeJSON(blob, &item); err != nil || item.ID == 0 || item.UserID == 0 {
		return
	}
	n.index(&item)
}
