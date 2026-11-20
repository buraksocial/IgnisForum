package flarum

type UserRow struct {
	ID               uint64  `json:"id"`
	Username         string  `json:"username"`
	Email            string  `json:"email"`
	EmailConfirmed   int     `json:"is_email_confirmed"`
	Password         string  `json:"password"`
	AvatarURL        *string `json:"avatar_url"`
	JoinedAt         *string `json:"joined_at"`
	LastSeenAt       *string `json:"last_seen_at"`
	SuspendedUntil   *string `json:"suspended_until"`
	SuspensionReason *string `json:"suspend_reason"`
}

type GroupRow struct {
	ID           uint64  `json:"id"`
	NameSingular string  `json:"name_singular"`
	NamePlural   string  `json:"name_plural"`
	Color        *string `json:"color"`
	Icon         *string `json:"icon"`
	Hidden       int     `json:"is_hidden"`
}

type GroupUserRow struct {
	UserID  uint64 `json:"user_id"`
	GroupID uint64 `json:"group_id"`
}

type DiscussionRow struct {
	ID             uint64  `json:"id"`
	Title          string  `json:"title"`
	CommentCount   int     `json:"comment_count"`
	CreatedAt      string  `json:"created_at"`
	UserID         *uint64 `json:"user_id"`
	FirstPostID    *uint64 `json:"first_post_id"`
	LastPostedAt   *string `json:"last_posted_at"`
	LastPostedUser *uint64 `json:"last_posted_user_id"`
	LastPostID     *uint64 `json:"last_post_id"`
	HiddenAt       *string `json:"hidden_at"`
	Slug           string  `json:"slug"`
	Private        int     `json:"is_private"`
	Approved       int     `json:"is_approved"`
	Sticky         int     `json:"is_sticky"`
	Locked         int     `json:"is_locked"`
}

type PostRow struct {
	ID         uint64  `json:"id"`
	Discussion uint64  `json:"discussion_id"`
	Number     uint64  `json:"number"`
	CreatedAt  string  `json:"created_at"`
	UserID     *uint64 `json:"user_id"`
	Type       string  `json:"type"`
	Content    *string `json:"content"`
	EditedAt   *string `json:"edited_at"`
	EditedUser *uint64 `json:"edited_user_id"`
	HiddenAt   *string `json:"hidden_at"`
	HiddenUser *uint64 `json:"hidden_user_id"`
	Private    int     `json:"is_private"`
	Approved   int     `json:"is_approved"`
}

type TagRow struct {
	ID          uint64  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Color       *string `json:"color"`
	Position    int     `json:"position"`
	ParentID    *uint64 `json:"parent_id"`
	Restricted  int     `json:"is_restricted"`
	Hidden      int     `json:"is_hidden"`
}

type DiscussionTagRow struct {
	DiscussionID uint64 `json:"discussion_id"`
	TagID        uint64 `json:"tag_id"`
}

type LikeRow struct {
	PostID uint64 `json:"post_id"`
	UserID uint64 `json:"user_id"`
}

type FlagRow struct {
	ID           uint64  `json:"id"`
	PostID       uint64  `json:"post_id"`
	Type         string  `json:"type"`
	UserID       *uint64 `json:"user_id"`
	Reason       *string `json:"reason"`
	ReasonDetail *string `json:"reason_detail"`
	CreatedAt    string  `json:"created_at"`
}

type NotificationRow struct {
	ID        uint64  `json:"id"`
	UserID    uint64  `json:"user_id"`
	FromUser  *uint64 `json:"from_user_id"`
	Type      string  `json:"type"`
	SubjectID *uint64 `json:"subject_id"`
	Data      any     `json:"data"`
	CreatedAt string  `json:"created_at"`
	Deleted   int     `json:"is_deleted"`
	ReadAt    *string `json:"read_at"`
}
