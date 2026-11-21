package flarum

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/buraksocial/IgnisForum/internal/auth"
	"github.com/buraksocial/IgnisForum/internal/bbcode"
	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

const (
	importHashKey   = "migration:flarum:snapshot_sha256"
	importReportKey = "migration:flarum:report"
)

type TableCount struct {
	Source   int `json:"source"`
	Imported int `json:"imported"`
}

type Exception struct {
	Table    string `json:"table"`
	SourceID uint64 `json:"source_id,omitempty"`
	Reason   string `json:"reason"`
}

type ImportReport struct {
	Version        int                   `json:"version"`
	Source         string                `json:"source"`
	SnapshotSHA256 string                `json:"snapshot_sha256"`
	Tables         map[string]TableCount `json:"tables"`
	Exceptions     []Exception           `json:"exceptions"`
	CompletedAt    time.Time             `json:"completed_at"`
}

type ImportOptions struct {
	// AvatarSourceDir points at Flarum public/assets/avatars. It is read-only.
	AvatarSourceDir string
	// AvatarDir is the private destination uploads/avatars directory.
	AvatarDir string
}

// Import applies a validated snapshot to an isolated IgnisForum store. Primary
// IDs for Flarum users, tags, discussions, and posts are retained exactly; the
// allocator is advanced above their high-water mark before synthetic records
// (such as a category and reactions) are created. Replaying an identical
// snapshot is idempotent. A different snapshot requires a fresh staging store.
func Import(snapshot *Snapshot, digest [32]byte, st *store.Store, repos *repo.Repos, users *auth.UserRepo, options ImportOptions) (*ImportReport, error) {
	if snapshot == nil || st == nil || repos == nil || users == nil {
		return nil, errors.New("import requires snapshot, store, repositories, and user repository")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	digestHex := hex.EncodeToString(digest[:])
	var priorHash string
	markerExists, err := st.GetJSON(importHashKey, &priorHash)
	if err != nil {
		return nil, err
	}
	if markerExists && priorHash != digestHex {
		return nil, errors.New("staging data directory was initialized with a different Flarum snapshot; use a fresh staging directory")
	}
	if !markerExists && existsNonempty(st) {
		return nil, errors.New("refusing to import into a non-empty staging store without a Flarum import marker")
	}

	report := &ImportReport{Version: 1, Source: snapshot.Source, SnapshotSHA256: digestHex, Tables: make(map[string]TableCount)}
	setSourceCounts(snapshot, report)
	permissions := perm.NewEngine(st)
	if err := permissions.Bootstrap(); err != nil {
		return nil, fmt.Errorf("bootstrap staging permissions: %w", err)
	}
	userRows, err := DecodeTable[UserRow](snapshot, "users")
	if err != nil {
		return nil, err
	}
	avatarSources := 0
	for _, user := range userRows {
		if user.AvatarURL != nil && strings.TrimSpace(*user.AvatarURL) != "" {
			avatarSources++
		}
	}
	report.Tables["avatars"] = TableCount{Source: avatarSources}
	groupRows, err := DecodeTable[GroupRow](snapshot, "groups")
	if err != nil {
		return nil, err
	}
	groupUserRows, err := DecodeTable[GroupUserRow](snapshot, "group_user")
	if err != nil {
		return nil, err
	}
	groupPermissionRows, err := DecodeTable[GroupPermissionRow](snapshot, "group_permission")
	if err != nil {
		return nil, err
	}
	tagRows, err := DecodeTable[TagRow](snapshot, "tags")
	if err != nil {
		return nil, err
	}
	discussionRows, err := DecodeTable[DiscussionRow](snapshot, "discussions")
	if err != nil {
		return nil, err
	}
	postRows, err := DecodeTable[PostRow](snapshot, "posts")
	if err != nil {
		return nil, err
	}
	discussionTagRows, err := DecodeTable[DiscussionTagRow](snapshot, "discussion_tag")
	if err != nil {
		return nil, err
	}
	var discussionUserRows []DiscussionUserRow
	if _, exists := snapshot.Tables["discussion_user"]; exists {
		discussionUserRows, err = DecodeTable[DiscussionUserRow](snapshot, "discussion_user")
		if err != nil {
			return nil, err
		}
	}
	likeRows, err := DecodeTable[LikeRow](snapshot, "post_likes")
	if err != nil {
		return nil, err
	}
	flagRows, err := DecodeTable[FlagRow](snapshot, "flags")
	if err != nil {
		return nil, err
	}

	maxID := maxSourceID(userRows, groupRows, tagRows, discussionRows, postRows, flagRows)
	seenGroups := make(map[uint64]bool)
	for _, group := range groupRows {
		name := strings.TrimSpace(group.NameSingular)
		if group.ID == 0 || seenGroups[group.ID] || (group.ID > 3 && (len(name) < 2 || len(name) > 80)) {
			return nil, fmt.Errorf("invalid or duplicate Flarum group %d", group.ID)
		}
		seenGroups[group.ID] = true
	}
	if maxID > ^uint64(0)-2 {
		return nil, errors.New("source ID space is exhausted")
	}
	rootCategoryID := maxID + 1
	uncategorizedID := maxID + 2
	st.AdvanceIDFloor(uncategorizedID)
	if !markerExists {
		// Persist the marker only after the snapshot has passed structural checks.
		if err := st.PutJSON(importHashKey, digestHex); err != nil {
			return nil, err
		}
	}

	groupMap := map[uint64]uint64{}
	for _, group := range groupRows {
		mapped, ok := mapGroup(group.ID)
		if !ok {
			custom, err := permissions.ImportGroup(group.ID, group.NameSingular, "Imported Flarum group")
			if err != nil {
				return nil, fmt.Errorf("import Flarum group %d: %w", group.ID, err)
			}
			mapped = custom.ID
		}
		groupMap[group.ID] = mapped
		report.Tables["groups"] = increment(report.Tables["groups"])
	}
	permissionGrants := make(map[uint64]map[string]bool)
	for _, sourceGrant := range groupPermissionRows {
		destinationGroup, mappedGroup := groupMap[sourceGrant.GroupID]
		permissionKey, supported := mapFlarumPermission(sourceGrant.Permission)
		if !mappedGroup || !supported {
			report.Exceptions = append(report.Exceptions, Exception{Table: "group_permission", SourceID: sourceGrant.GroupID, Reason: fmt.Sprintf("Flarum permission %q is not represented by IgnisForum", sourceGrant.Permission)})
			continue
		}
		grant := permissionGrants[destinationGroup]
		if grant == nil {
			grant = permissions.GetGrant(destinationGroup, perm.ScopeGlobal, 0)
			if grant == nil {
				grant = make(map[string]bool)
			}
			permissionGrants[destinationGroup] = grant
		}
		grant[permissionKey] = true
		report.Tables["group_permission"] = increment(report.Tables["group_permission"])
	}
	for groupID, grant := range permissionGrants {
		if err := permissions.SetGrant(groupID, perm.ScopeGlobal, 0, grant); err != nil {
			return nil, fmt.Errorf("apply mapped Flarum permission group %d: %w", groupID, err)
		}
	}
	userGroups := make(map[uint64][]uint64)
	for _, membership := range groupUserRows {
		if groupID, ok := groupMap[membership.GroupID]; ok && groupID != perm.GroupGuest {
			userGroups[membership.UserID] = appendUnique(userGroups[membership.UserID], groupID)
			report.Tables["group_user"] = increment(report.Tables["group_user"])
		} else {
			report.Exceptions = append(report.Exceptions, Exception{Table: "group_user", SourceID: membership.UserID, Reason: fmt.Sprintf("membership in unmapped or guest group %d was omitted", membership.GroupID)})
		}
	}
	for _, user := range userRows {
		if user.ID == 0 || strings.TrimSpace(user.Username) == "" {
			return nil, fmt.Errorf("invalid Flarum user row id=%d", user.ID)
		}
		// Flarum's Member role is implicit for every registered account,
		// including moderators and users with explicit custom memberships.
		groups := appendUnique(userGroups[user.ID], perm.GroupMember)
		previous, hadPrevious := users.GetByID(user.ID)
		if hadPrevious && (!strings.EqualFold(previous.Username, user.Username) || previous.EmailHash != auth.HashEmail(user.Email)) {
			return nil, fmt.Errorf("source user %d conflicts with an existing staging identity", user.ID)
		}
		usr, err := users.CreateUserDirect(user.ID, user.Username, user.Email, user.Password, groups, 0)
		if err != nil {
			return nil, fmt.Errorf("import user %d: %w", user.ID, err)
		}
		// Successful legacy logins upgrade bcrypt hashes to Argon2id. A resumed
		// import must not downgrade that credential back to the snapshot hash.
		if hadPrevious && strings.HasPrefix(previous.PasswordHash, "$argon2id$") {
			usr.PasswordHash = previous.PasswordHash
		}
		if user.SuspendedUntil != nil && *user.SuspendedUntil != "" {
			usr.State = 2
			report.Exceptions = append(report.Exceptions, Exception{Table: "users", SourceID: user.ID, Reason: "suspended account retained disabled"})
		}
		if user.AvatarURL != nil && strings.TrimSpace(*user.AvatarURL) != "" {
			if options.AvatarSourceDir == "" || options.AvatarDir == "" {
				report.Exceptions = append(report.Exceptions, Exception{Table: "avatars", SourceID: user.ID, Reason: "Flarum avatar exists but no source/destination avatar directories were supplied"})
			} else if avatarName, err := copyAvatar(options.AvatarSourceDir, options.AvatarDir, user.ID, *user.AvatarURL); err != nil {
				report.Exceptions = append(report.Exceptions, Exception{Table: "avatars", SourceID: user.ID, Reason: "avatar not imported: " + err.Error()})
			} else {
				usr.AvatarPath = avatarName
				report.Tables["avatars"] = increment(report.Tables["avatars"])
			}
		}
		if t, ok := parseOptionalTime(user.JoinedAt); ok {
			usr.JoinedAt = t
		}
		if t, ok := parseOptionalTime(user.LastSeenAt); ok {
			usr.LastSeen = t
		}
		if user.EmailConfirmed == 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: "users", SourceID: user.ID, Reason: "email-confirmation status is not modeled in IgnisForum"})
		}
		if err := st.PutJSON(fmt.Sprintf("user:%d", usr.ID), usr); err != nil {
			return nil, err
		}
		report.Tables["users"] = increment(report.Tables["users"])
	}

	category := &domain.Node{ID: rootCategoryID, Type: domain.TypeCategory, Title: "Flarum discussions", Slug: "flarum-discussions", Position: 0}
	if _, err := repos.Nodes.ImportNode(category.ID, category.Type, 0, category.Title, category.Slug, "Imported from Flarum", 0); err != nil {
		return nil, err
	}
	for i, tag := range sortedTags(tagRows) {
		if tag.ID == rootCategoryID || tag.ID == uncategorizedID {
			return nil, fmt.Errorf("source tag id %d collides with synthetic node", tag.ID)
		}
		title := strings.TrimSpace(tag.Name)
		if title == "" {
			title = fmt.Sprintf("Tag %d", tag.ID)
		}
		if tag.ParentID != nil {
			if parent, ok := findTag(tagRows, *tag.ParentID); ok {
				title = strings.TrimSpace(parent.Name) + " / " + title
				report.Exceptions = append(report.Exceptions, Exception{Table: "tags", SourceID: tag.ID, Reason: "nested tag flattened into a forum title"})
			}
		}
		if tag.Restricted != 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: "tags", SourceID: tag.ID, Reason: "restricted-tag discussions are hidden to avoid broadening source access"})
		}
		if tag.Hidden != 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: "tags", SourceID: tag.ID, Reason: "hidden-tag visibility is not modeled"})
		}
		if _, err := repos.Nodes.ImportNode(tag.ID, domain.TypeForum, rootCategoryID, title, tag.Slug, stringValue(tag.Description), i); err != nil {
			return nil, err
		}
		report.Tables["tags"] = increment(report.Tables["tags"])
	}
	if len(tagRows) == 0 {
		if _, err := repos.Nodes.ImportNode(uncategorizedID, domain.TypeForum, rootCategoryID, "Uncategorized", "uncategorized", "Imported discussions without a Flarum tag", 0); err != nil {
			return nil, err
		}
	}

	tagsByDiscussion := make(map[uint64][]uint64)
	for _, relation := range discussionTagRows {
		if _, ok := findTag(tagRows, relation.TagID); !ok {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_tag", SourceID: relation.DiscussionID, Reason: fmt.Sprintf("unknown source tag %d", relation.TagID)})
			continue
		}
		tagsByDiscussion[relation.DiscussionID] = appendUnique(tagsByDiscussion[relation.DiscussionID], relation.TagID)
		report.Tables["discussion_tag"] = increment(report.Tables["discussion_tag"])
	}
	postsByDiscussion := make(map[uint64][]PostRow)
	postByID := make(map[uint64]PostRow, len(postRows))
	for _, post := range postRows {
		postsByDiscussion[post.Discussion] = append(postsByDiscussion[post.Discussion], post)
		postByID[post.ID] = post
	}
	for did := range postsByDiscussion {
		sort.Slice(postsByDiscussion[did], func(i, j int) bool {
			if postsByDiscussion[did][i].Number == postsByDiscussion[did][j].Number {
				return postsByDiscussion[did][i].ID < postsByDiscussion[did][j].ID
			}
			return postsByDiscussion[did][i].Number < postsByDiscussion[did][j].Number
		})
	}
	for _, discussion := range discussionRows {
		if discussion.FirstPostID == nil || *discussion.FirstPostID == 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussions", SourceID: discussion.ID, Reason: "discussion has no first post reference"})
			continue
		}
		first, ok := postByID[*discussion.FirstPostID]
		if !ok || first.Discussion != discussion.ID || first.Type != "comment" {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussions", SourceID: discussion.ID, Reason: "first post is missing or has an unsupported type"})
			continue
		}
		discussionPosts := postsByDiscussion[discussion.ID]
		if len(discussionPosts) == 0 {
			continue
		}
		nodeID := uncategorizedID
		discussionTags := tagsByDiscussion[discussion.ID]
		if len(discussionTags) > 0 {
			sort.Slice(discussionTags, func(i, j int) bool {
				a, _ := findTag(tagRows, discussionTags[i])
				b, _ := findTag(tagRows, discussionTags[j])
				if a.Position == b.Position {
					return a.ID < b.ID
				}
				return a.Position < b.Position
			})
			nodeID = discussionTags[0]
			if len(discussionTags) > 1 {
				report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_tag", SourceID: discussion.ID, Reason: "multiple tags retained on the thread; its primary forum is the first Flarum tag"})
			}
		} else if len(tagRows) > 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_tag", SourceID: discussion.ID, Reason: "discussion has no tag; placed in uncategorized forum"})
			nodeID = ensureUncategorized(repos, uncategorizedID, rootCategoryID, len(tagRows))
		}
		ownerID := optionalID(discussion.UserID)
		status := domain.ThreadOpen
		if discussion.Locked != 0 {
			status = domain.ThreadClosed
		}
		private := discussion.Private != 0
		for _, tagID := range discussionTags {
			tag, _ := findTag(tagRows, tagID)
			if tag.Restricted != 0 {
				private = true
			}
		}
		discussionState := domain.DiscussionVisible
		if discussion.Approved == 0 {
			discussionState = domain.DiscussionPending
		}
		if discussion.HiddenAt != nil {
			discussionState = domain.DiscussionHidden
		}
		if private {
			status = domain.ThreadDeleted
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussions", SourceID: discussion.ID, Reason: "private or restricted discussion retained but excluded from public indexes"})
		} else if discussion.HiddenAt != nil {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussions", SourceID: discussion.ID, Reason: "hidden discussion retained in a reversible moderation state"})
		} else if discussion.Approved == 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussions", SourceID: discussion.ID, Reason: "unapproved discussion retained in the pending moderation queue"})
		}
		title := strings.TrimSpace(discussion.Title)
		if title == "" {
			title = fmt.Sprintf("Untitled discussion %d", discussion.ID)
		}
		createdAt, err := parseTime(discussion.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("discussion %d date: %w", discussion.ID, err)
		}
		posts := make([]*domain.Post, 0, len(discussionPosts))
		threadPostCount := 0
		threadLastPost := (*domain.LastPostRef)(nil)
		for _, sourcePost := range discussionPosts {
			if sourcePost.Type != "comment" {
				report.Exceptions = append(report.Exceptions, Exception{Table: "posts", SourceID: sourcePost.ID, Reason: "unsupported Flarum post type"})
				continue
			}
			date, err := parseTime(sourcePost.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("post %d date: %w", sourcePost.ID, err)
			}
			body := ""
			if sourcePost.Content != nil {
				body = *sourcePost.Content
			}
			state := domain.PostVisible
			if private || sourcePost.Private != 0 {
				state = domain.PostDeleted
			} else if sourcePost.HiddenAt != nil {
				state = domain.PostHidden
			} else if discussionState == domain.DiscussionPending || sourcePost.Approved == 0 {
				state = domain.PostPending
			}
			if state == domain.PostHidden {
				report.Exceptions = append(report.Exceptions, Exception{Table: "posts", SourceID: sourcePost.ID, Reason: "hidden post retained in a reversible moderation state"})
			} else if state == domain.PostPending {
				report.Exceptions = append(report.Exceptions, Exception{Table: "posts", SourceID: sourcePost.ID, Reason: "unapproved post retained in the pending moderation queue"})
			} else if state == domain.PostDeleted {
				report.Exceptions = append(report.Exceptions, Exception{Table: "posts", SourceID: sourcePost.ID, Reason: "private post retained as unavailable"})
			}
			userID := optionalID(sourcePost.UserID)
			post := &domain.Post{ID: sourcePost.ID, ThreadID: discussion.ID, UserID: userID,
				MessageRaw: body, MessageRendered: SafePostHTML(body), Date: date, State: state, Version: 1}
			if sourcePost.EditedAt != nil {
				if edited, ok := parseOptionalTime(sourcePost.EditedAt); ok {
					post.EditedAt, post.EditCount = edited, 1
				}
			}
			posts = append(posts, post)
			if state == domain.PostVisible {
				threadPostCount++
				if threadLastPost == nil || date.After(threadLastPost.T) {
					threadLastPost = &domain.LastPostRef{T: date, PostID: post.ID, UserID: post.UserID}
				}
			}
			report.Tables["posts"] = increment(report.Tables["posts"])
		}
		foundFirst := false
		for _, post := range posts {
			if post.ID == *discussion.FirstPostID {
				foundFirst = true
			}
		}
		if !foundFirst {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussions", SourceID: discussion.ID, Reason: "first post could not be imported"})
			continue
		}
		if threadLastPost == nil && len(posts) > 0 {
			p := posts[0]
			threadLastPost = &domain.LastPostRef{T: p.Date, PostID: p.ID, UserID: p.UserID}
		}
		thread := &domain.Thread{ID: discussion.ID, NodeID: nodeID, Title: title, Slug: fmt.Sprintf("%s-%d", bbcode.Slug(title), discussion.ID),
			TagIDs: append([]uint64(nil), discussionTags...), Sticky: discussion.Sticky != 0,
			UserID: ownerID, FirstPostID: *discussion.FirstPostID,
			PostCount: threadPostCount, Status: status, DiscussionState: discussionState, LastPost: threadLastPost, Date: createdAt, Version: 1}
		if len(discussionTags) > 0 && nodeID != discussionTags[0] { /* tag list is retained in Thread.TagIDs */
		}
		if err := repos.Threads.Import(thread, posts); err != nil {
			return nil, fmt.Errorf("import discussion %d: %w", discussion.ID, err)
		}
		report.Tables["discussions"] = increment(report.Tables["discussions"])
	}

	for _, like := range likeRows {
		post, ok := repos.Posts.Get(like.PostID)
		_, userExists := users.GetByID(like.UserID)
		if !ok || post.State != 0 || !userExists {
			report.Exceptions = append(report.Exceptions, Exception{Table: "post_likes", SourceID: like.PostID, Reason: "like references missing, hidden, or unmapped content"})
			continue
		}
		if current, exists := repos.Reactions.Get(like.PostID, like.UserID); !exists || current != "like" {
			if _, err := repos.Reactions.Toggle(like.PostID, like.UserID, "like"); err != nil {
				return nil, fmt.Errorf("import like for post %d: %w", like.PostID, err)
			}
		}
		report.Tables["post_likes"] = increment(report.Tables["post_likes"])
	}
	for _, flag := range flagRows {
		_, reporterExists := users.GetByID(optionalID(flag.UserID))
		if flag.UserID == nil || !reporterExists {
			report.Exceptions = append(report.Exceptions, Exception{Table: "flags", SourceID: flag.ID, Reason: "flag reporter is missing or unmapped"})
			continue
		}
		reason := stringValue(flag.Reason)
		if detail := stringValue(flag.ReasonDetail); detail != "" {
			if reason != "" {
				reason += ": "
			}
			reason += detail
		}
		if strings.TrimSpace(reason) == "" {
			reason = "Flarum report: " + flag.Type
		}
		if len(reason) > 500 {
			reason = reason[:500]
		}
		if _, err := repos.CreateReport(flag.PostID, *flag.UserID, ensureReportReason(reason)); err != nil {
			report.Exceptions = append(report.Exceptions, Exception{Table: "flags", SourceID: flag.ID, Reason: "flag could not be represented as a visible post report"})
			continue
		}
		report.Tables["flags"] = increment(report.Tables["flags"])
	}
	for _, sourceState := range discussionUserRows {
		if sourceState.UserID == 0 || sourceState.DiscussionID == 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_user", SourceID: sourceState.UserID, Reason: "read/subscription state has an invalid user or discussion ID"})
			continue
		}
		if _, ok := users.GetByID(sourceState.UserID); !ok {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_user", SourceID: sourceState.UserID, Reason: fmt.Sprintf("state references unmapped user %d", sourceState.UserID)})
			continue
		}
		if _, ok := repos.Threads.Get(sourceState.DiscussionID); !ok {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_user", SourceID: sourceState.UserID, Reason: fmt.Sprintf("state references unmapped discussion %d", sourceState.DiscussionID)})
			continue
		}
		subscription := ""
		if sourceState.Subscription != nil {
			subscription = strings.ToLower(strings.TrimSpace(*sourceState.Subscription))
		}
		if subscription != "" && subscription != "follow" && subscription != "ignore" {
			report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_user", SourceID: sourceState.UserID, Reason: "unsupported discussion subscription value"})
			continue
		}
		state := &domain.ThreadUserState{UserID: sourceState.UserID, ThreadID: sourceState.DiscussionID, Subscription: subscription}
		if sourceState.LastReadAt != nil {
			if strings.TrimSpace(*sourceState.LastReadAt) != "" {
				lastRead, ok := parseOptionalTime(sourceState.LastReadAt)
				if !ok {
					report.Exceptions = append(report.Exceptions, Exception{Table: "discussion_user", SourceID: sourceState.UserID, Reason: "invalid last-read timestamp"})
					continue
				}
				state.LastReadAt = lastRead
			}
		}
		if sourceState.LastReadPostNumber != nil {
			state.LastReadPostNumber = *sourceState.LastReadPostNumber
		}
		if err := repos.PutThreadUserState(state); err != nil {
			return nil, fmt.Errorf("import discussion user state %d/%d: %w", state.ThreadID, state.UserID, err)
		}
		report.Tables["discussion_user"] = increment(report.Tables["discussion_user"])
	}

	// These source relations have no current IgnisForum representation. Preserve
	// their omission in the report instead of silently implying parity.
	for _, table := range []string{"post_user", "post_mentions_group", "post_mentions_post", "post_mentions_tag", "post_mentions_user", "tag_user", "notifications"} {
		count := report.Tables[table].Source
		if count > 0 {
			report.Exceptions = append(report.Exceptions, Exception{Table: table, Reason: fmt.Sprintf("%d source rows are not represented by IgnisForum", count)})
		}
	}
	st.RebuildContent(repos.Hooks)
	if err := repos.Nodes.RebuildContentCounts(); err != nil {
		return nil, err
	}
	if err := repos.Nodes.BuildTree(); err != nil {
		return nil, err
	}
	report.CompletedAt = time.Now().UTC()
	if err := st.PutJSON(importReportKey, report); err != nil {
		return nil, err
	}
	st.Flush()
	return report, nil
}

func existsNonempty(st *store.Store) bool {
	for _, typ := range []string{store.TypeUser, store.TypeNode, store.TypeThread, store.TypePost, store.TypePostReaction, store.TypeReport} {
		if st.MaxID(typ) != 0 {
			return true
		}
	}
	return false
}

func setSourceCounts(s *Snapshot, report *ImportReport) {
	for name, raw := range s.Tables {
		var table tableEnvelope
		_ = json.Unmarshal(raw, &table)
		report.Tables[name] = TableCount{Source: table.SourceCount}
	}
}

func increment(c TableCount) TableCount { c.Imported++; return c }

func mapGroup(sourceID uint64) (uint64, bool) {
	// Flarum reserves IDs 1/2/3 for administrator/guest/member. Names are
	// editable and must never be used to infer privileged membership.
	switch sourceID {
	case 1:
		return perm.GroupAdmin, true
	case 3:
		return perm.GroupMember, true
	case 2:
		return perm.GroupGuest, true
	default:
		return 0, false
	}
}

func mapFlarumPermission(name string) (string, bool) {
	switch name {
	case "viewForum":
		return perm.KeyForumView, true
	case "startDiscussion":
		return perm.KeyForumThread, true
	case "discussion.reply":
		return perm.KeyForumReply, true
	case "discussion.flagPosts":
		return perm.KeyPostReport, true
	case "discussion.likePosts":
		return perm.KeyPostReact, true
	case "discussion.replyWithoutApproval":
		return perm.KeyReplyNoApproval, true
	case "discussion.startWithoutApproval":
		return perm.KeyThreadNoApproval, true
	case "searchUsers":
		return perm.KeySearchUsers, true
	case "discussion.approvePosts":
		return perm.KeyPostApprove, true
	case "discussion.editPosts":
		return perm.KeyPostEditAny, true
	case "discussion.hide":
		return perm.KeyThreadHide, true
	case "discussion.hidePosts":
		return perm.KeyPostHide, true
	case "discussion.lock":
		return perm.KeyThreadLock, true
	case "discussion.rename":
		return perm.KeyThreadRename, true
	case "discussion.sticky":
		return perm.KeyThreadSticky, true
	case "discussion.tag":
		return perm.KeyThreadTag, true
	case "discussion.viewFlags":
		return perm.KeyViewFlags, true
	case "discussion.viewIpsPosts":
		return perm.KeyViewPostIP, true
	case "user.suspend":
		return perm.KeyUserSuspend, true
	case "user.viewLastSeenAt":
		return perm.KeyUserLastSeen, true
	default:
		return "", false
	}
}

func copyAvatar(sourceDir, targetDir string, userID uint64, sourceName string) (string, error) {
	if parsed, err := url.Parse(sourceName); err == nil && parsed.Path != "" {
		sourceName = filepath.Base(parsed.Path)
	}
	if userID == 0 || sourceName == "" || filepath.Base(sourceName) != sourceName || strings.ContainsAny(sourceName, "/\\\x00") || len(sourceName) > 255 {
		return "", errors.New("unsafe source filename")
	}
	sourceInfo, err := os.Lstat(sourceDir)
	if err != nil || !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("avatar source directory is unavailable or unsafe")
	}
	sourcePath := filepath.Join(sourceDir, sourceName)
	fileInfo, err := os.Lstat(sourcePath)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() < 1 || fileInfo.Size() > 8<<20 {
		return "", errors.New("avatar file is missing, not regular, or exceeds 8 MiB")
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	openedInfo, err := f.Stat()
	if err != nil || !os.SameFile(fileInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		f.Close()
		return "", errors.New("avatar changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(data) == 0 || len(data) > 8<<20 {
		return "", errors.New("avatar is empty or exceeds 8 MiB")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 16_000_000 {
		return "", errors.New("avatar is not a supported image or dimensions exceed limits")
	}
	extension := map[string]string{"png": ".png", "jpeg": ".jpg", "gif": ".gif"}[format]
	if extension == "" {
		return "", errors.New("avatar image format is not supported")
	}
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return "", err
	}
	targetInfo, err := os.Lstat(targetDir)
	if err != nil || !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("avatar destination directory is unsafe")
	}
	if err := os.Chmod(targetDir, 0700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("u%d%s", userID, extension)
	targetPath := filepath.Join(targetDir, name)
	if existing, err := os.Lstat(targetPath); err == nil {
		if !existing.Mode().IsRegular() {
			return "", errors.New("existing avatar destination is not a regular file")
		}
		old, err := os.ReadFile(targetPath)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(old, data) {
			return "", errors.New("existing avatar content differs from source")
		}
		return name, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		os.Remove(targetPath)
		return "", err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(targetPath)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(targetPath)
		return "", err
	}
	return name, nil
}

func maxSourceID(users []UserRow, groups []GroupRow, tags []TagRow, discussions []DiscussionRow, posts []PostRow, flags []FlagRow) uint64 {
	var max uint64
	check := func(id uint64) {
		if id > max {
			max = id
		}
	}
	for _, row := range users {
		check(row.ID)
	}
	for _, row := range groups {
		check(row.ID)
	}
	for _, row := range tags {
		check(row.ID)
	}
	for _, row := range discussions {
		check(row.ID)
	}
	for _, row := range posts {
		check(row.ID)
	}
	for _, row := range flags {
		check(row.ID)
	}
	return max
}

func sortedTags(rows []TagRow) []TagRow {
	copy := append([]TagRow(nil), rows...)
	sort.Slice(copy, func(i, j int) bool {
		if copy[i].Position == copy[j].Position {
			return copy[i].ID < copy[j].ID
		}
		return copy[i].Position < copy[j].Position
	})
	return copy
}

func findTag(rows []TagRow, id uint64) (TagRow, bool) {
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return TagRow{}, false
}

func appendUnique(values []uint64, value uint64) []uint64 {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func optionalID(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

func parseOptionalTime(raw *string) (time.Time, bool) {
	if raw == nil || *raw == "" {
		return time.Time{}, false
	}
	value, err := parseTime(*raw)
	return value, err == nil
}

func parseTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02 15:04:05.000000"} {
		if value, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return value.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", raw)
}

func ensureReportReason(reason string) string {
	if len(strings.TrimSpace(reason)) < 3 {
		return "Flarum moderator report"
	}
	return reason
}

func ensureUncategorized(repos *repo.Repos, id, parent uint64, position int) uint64 {
	if _, ok := repos.Nodes.Get(id); !ok {
		_, _ = repos.Nodes.ImportNode(id, domain.TypeForum, parent, "Uncategorized", "uncategorized", "Imported discussions without a Flarum tag", position)
	}
	return id
}
