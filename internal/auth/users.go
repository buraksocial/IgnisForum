package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// Registration / login errors. Login never distinguishes unknown-user from
// wrong-password (ErrBadCredentials only) to keep the response a oracle-free.
var (
	ErrUsernameTaken = errors.New("auth: kullanıcı adı alınmış")
	ErrEmailTaken    = errors.New("auth: bu e-posta zaten kayıtlı")
	ErrBadUsername   = errors.New("auth: kullanıcı adı geçersiz")
	ErrBadEmail      = errors.New("auth: e-posta geçersiz")
	ErrShortPassword = errors.New("auth: şifre en az 8 karakter olmalı")
	ErrUserBanned    = errors.New("auth: hesap etkin değil")
	ErrNotFound      = errors.New("auth: kullanıcı yok")
)

// Re-exported group ids (canonical definition lives in perm, which owns the
// permission engine; keeping one definition avoids import cycles).
const (
	GroupGuest  = perm.GroupGuest
	GroupMember = perm.GroupMember
	GroupAdmin  = perm.GroupAdmin
)

// UserRepo owns the user primary + the two uniqueness lookup records. Write
// order follows §3.6: user blob (primary) first, then username/email lookups —
// a crash in between can only orphan a lookup, never half-write a user.
type UserRepo struct {
	s  *Service
	rl *RateLimiter
	// dummyHash absorbs the argon2 cost when the username is unknown, so the
	// login response time does not leak which accounts exist.
	dummyHash string
}

// NewUserRepo wires the user service; dummyHash is computed once (argon2id).
func NewUserRepo(s *Service, rl *RateLimiter) *UserRepo {
	dh, err := s.HashPassword("ignisforum-timing-equaliser-" + time.Now().String())
	if err != nil {
		dh = "" // never in practice (crypto/rand failure)
	}
	return &UserRepo{s: s, rl: rl, dummyHash: dh}
}

func keyUser(id uint64) string        { return fmt.Sprintf("user:%d", id) }
func keyUsername(lower string) string { return "username:" + lower }
func keyEmail(hash string) string     { return "useremail:" + hash }

// GetByID returns the user blob (false on miss).
func (u *UserRepo) GetByID(id uint64) (*domain.User, bool) {
	var usr domain.User
	ok, _ := u.s.s.GetJSON(keyUser(id), &usr)
	if !ok {
		return nil, false
	}
	return &usr, true
}

// ByName resolves a login identifier: a username, or (contains '@') an email.
func (u *UserRepo) ByName(name string) (*domain.User, bool) {
	name = strings.TrimSpace(name)
	if strings.Contains(name, "@") {
		var id uint64
		if ok, _ := u.s.s.GetJSON(keyEmail(HashEmail(name)), &id); !ok || id == 0 {
			return nil, false
		}
		return u.GetByID(id)
	}
	var id uint64
	if ok, _ := u.s.s.GetJSON(keyUsername(strings.ToLower(name)), &id); !ok || id == 0 {
		return nil, false
	}
	return u.GetByID(id)
}

// Username returns the display name for uid ("" on miss) — the lookup that
// replaces the M1 stub in the web layer.
func (u *UserRepo) Username(id uint64) string {
	if usr, ok := u.GetByID(id); ok {
		return usr.Username
	}
	return ""
}

// validUsername: letters/digits/._- , 2-30 chars, must start with a letter or
// digit. Turkish letters are allowed (unicode letters).
func validUsername(name string) bool {
	if len(name) < 2 || len(name) > 30 {
		return false
	}
	first := name[0]
	if !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z' || first >= '0' && first <= '9' || first >= 0xC0) {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		case r > 127: // unicode letters (Türkçe)
		default:
			return false
		}
	}
	return true
}

// Register creates a user. The caller has already gated the request (CSRF +
// rate limit); this enforces the uniqueness invariants and password policy.
func (u *UserRepo) Register(username, email, password string) (*domain.User, error) {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if !validUsername(username) {
		return nil, ErrBadUsername
	}
	if len(password) < 8 {
		return nil, ErrShortPassword
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, ErrBadEmail
	}

	lower := strings.ToLower(username)
	if ok, _ := u.s.s.GetJSON(keyUsername(lower), new(uint64)); ok {
		return nil, ErrUsernameTaken
	}
	eh := HashEmail(email)
	if ok, _ := u.s.s.GetJSON(keyEmail(eh), new(uint64)); ok {
		return nil, ErrEmailTaken
	}

	hash, err := u.s.HashPassword(password)
	if err != nil {
		return nil, err
	}
	id := u.s.s.NextID()
	u.s.s.MarkID(store.TypeUser, id)

	usr := &domain.User{
		ID: id, Username: username, EmailHash: eh, PasswordHash: hash,
		State: 0, GroupIDs: []uint64{GroupMember},
		Timezone: "Europe/Istanbul", Locale: "tr",
		JoinedAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}
	// PRIMARY FIRST, then lookups.
	if err := u.s.s.PutJSON(keyUser(id), usr); err != nil {
		return nil, err
	}
	if err := u.s.s.PutJSON(keyUsername(lower), id); err != nil {
		return nil, err
	}
	if err := u.s.s.PutJSON(keyEmail(eh), id); err != nil {
		return nil, err
	}
	return usr, nil
}

// CreateUserDirect creates a user with a known id/password hash (bootstrap:
// the admin account and the seed demo users). Same write order as Register.
func (u *UserRepo) CreateUserDirect(id uint64, username, email, passwordHash string, groups []uint64, wpID int) (*domain.User, error) {
	usr := &domain.User{
		ID: id, Username: username, EmailHash: HashEmail(email), PasswordHash: passwordHash,
		State: 0, GroupIDs: groups,
		Timezone: "Europe/Istanbul", Locale: "tr",
		JoinedAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
		WPUserID: wpID,
	}
	if err := u.s.s.PutJSON(keyUser(id), usr); err != nil {
		return nil, err
	}
	u.s.s.MarkID(store.TypeUser, id)
	if err := u.s.s.PutJSON(keyUsername(strings.ToLower(username)), id); err != nil {
		return nil, err
	}
	if err := u.s.s.PutJSON(keyEmail(usr.EmailHash), id); err != nil {
		return nil, err
	}
	return usr, nil
}

// SetPassword replaces a user's password hash. It is used by the local
// administrator recovery command and never returns or logs the plaintext.
func (u *UserRepo) SetPassword(id uint64, password string) error {
	if len(password) < 16 {
		return errors.New("auth: password must contain at least 16 characters")
	}
	usr, ok := u.GetByID(id)
	if !ok {
		return ErrNotFound
	}
	hash, err := u.s.HashPassword(password)
	if err != nil {
		return err
	}
	usr.PasswordHash = hash
	usr.State = 0
	return u.s.s.PutJSON(keyUser(id), usr)
}

// Login verifies credentials and mints a session. Rate limiting is the
// caller's responsibility around this (web layer checks both buckets); this
// only clears the buckets on success.
//
// Timing: both branches run one argon2 verification (real hash or dummy), so
// unknown users and wrong passwords take the same time.
func (u *UserRepo) Login(identifier, password string, remember bool, ip string) (*domain.User, *Session, error) {
	usr, found := u.ByName(identifier)
	if !found || usr.State != 0 {
		if u.dummyHash != "" {
			u.s.VerifyPassword(password, u.dummyHash) // burn the same time
		}
		return nil, nil, ErrBadCredentials
	}
	if !u.s.VerifyPassword(password, usr.PasswordHash) {
		return nil, nil, ErrBadCredentials
	}

	// Success clears the brute-force counters for this identifier + IP.
	u.rl.Clear("login", "ip:"+HashIP(ip))
	u.rl.Clear("login", "id:"+strings.ToLower(strings.TrimSpace(identifier)))

	// Last-seen touch (KV blob RMW).
	usr.LastSeen = time.Now().UTC()
	_ = u.s.s.PutJSON(keyUser(usr.ID), usr)

	sess, err := u.s.CreateSession(usr.ID, remember, ip)
	if err != nil {
		return nil, nil, err
	}
	return usr, sess, nil
}
