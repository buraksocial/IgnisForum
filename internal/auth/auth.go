// Package auth implements M2 authentication: argon2id password hashing, opaque
// server-side sessions (KV blobs, TTL-checked on read), CSRF tokens bound to
// the session, and ZSET sliding-window rate limiting. No JWT, no in-memory
// session map — everything lives in the store so a restart keeps users logged
// in and the single-writer rule (§3.5) is never broken.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"

	"github.com/buraksocial/IgnisForum/internal/store"
)

// Session lifetimes.
const (
	SessionTTL      = 14 * 24 * time.Hour // "beni hatırla"
	SessionShortTTL = 12 * time.Hour      // plain login
	CSRFBytes       = 16
)

// Session is a persisted session record under session:{sid}.
type Session struct {
	SID        string    `json:"sid"`
	UserID     uint64    `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	CSRF       string    `json:"csrf"`
	Remember   bool      `json:"remember"`
	LastSeenAt time.Time `json:"last_seen_at"`
	IPHash     string    `json:"ip_h"`
}

// ErrBadCredentials is returned for any login failure (unknown user OR wrong
// password) so the response never distinguishes them.
var ErrBadCredentials = errors.New("auth: geçersiz kullanıcı adı veya şifre")

// Service owns sessions + hashing over the store.
type Service struct {
	s *store.Store
	// argon2id parameters — OWASP minimums, tuned for a 4-core box
	// (64MB, t=2, p=4 ≈ 40-70ms per login; login is rare, registration
	// even rarer, and it never happens on the render path).
	time, memory uint32
	threads      uint8
	keyLen       uint32
}

// NewService builds the auth service.
func NewService(s *store.Store) *Service {
	return &Service{s: s, time: 2, memory: 64 * 1024, threads: 4, keyLen: 32}
}

// HashPassword returns an encoded argon2id hash:
// $argon2id$v=19$m=65536,t=2,p=4$<salt b64>$<key b64>
func (a *Service) HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, a.time, a.memory, a.threads, a.keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		a.memory, a.time, a.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an encoded hash in constant time.
func (a *Service) VerifyPassword(password, encoded string) bool {
	// Flarum (PHP password_hash) stores bcrypt hashes with the $2y$ prefix.
	// Go's bcrypt parser accepts the equivalent $2a$ variant; the prefix is
	// normalized only in memory and the original hash remains untouched until
	// Login performs the successful Argon2id rehash.
	if strings.HasPrefix(encoded, "$2y$") || strings.HasPrefix(encoded, "$2b$") {
		encoded = "$2a$" + encoded[4:]
	}
	if strings.HasPrefix(encoded, "$2a$") {
		cost, err := bcrypt.Cost([]byte(encoded))
		// PHP bcrypt hashes in Flarum use ordinary costs (typically 10).
		// Bound attacker- or import-controlled values before bcrypt's exponential
		// work factor is evaluated by CompareHashAndPassword.
		if err != nil || cost < bcrypt.MinCost || cost > 16 {
			return false
		}
		return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
	}
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var m, t uint32
	var p uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || n != 3 {
		return false
	}
	// Encodings are persisted and may originate outside this service. Refuse
	// unreasonable parameters before allocating Argon2's memory buffer.
	if m < 8*1024 || m > 256*1024 || t == 0 || t > 10 || p == 0 || p > 8 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// randomHex returns n random bytes hex-encoded (2n chars).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateSession persists a session for userID and returns it (sid + CSRF).
func (a *Service) CreateSession(userID uint64, remember bool, ip string) (*Session, error) {
	sid, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	csrf, err := randomHex(CSRFBytes)
	if err != nil {
		return nil, err
	}
	ttl := SessionShortTTL
	if remember {
		ttl = SessionTTL
	}
	now := time.Now().UTC()
	sess := &Session{
		SID: sid, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(ttl),
		CSRF: csrf, Remember: remember, LastSeenAt: now, IPHash: HashIP(ip),
	}
	if err := a.s.PutJSON("session:"+sid, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// GetSession loads a live session by sid (nil if missing/expired). Expired
// sessions are deleted lazily on read — no scanner needed.
func (a *Service) GetSession(sid string) *Session {
	var sess Session
	ok, err := a.s.GetJSON("session:"+sid, &sess)
	if !ok || err != nil {
		return nil
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		a.s.Delete("session:" + sid)
		return nil
	}
	// Touch last_seen (bounded: once a minute).
	if time.Since(sess.LastSeenAt) > time.Minute {
		sess.LastSeenAt = time.Now().UTC()
		_ = a.s.PutJSON("session:"+sid, &sess)
	}
	return &sess
}

// DestroySession removes the session record (logout).
func (a *Service) DestroySession(sid string) {
	a.s.Delete("session:" + sid)
}

// ValidateCSRF is a constant-time comparison against the session token.
func ValidateCSRF(sess *Session, token string) bool {
	if sess == nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sess.CSRF), []byte(token)) == 1
}

// HashIP stores only a salted one-way hash of the client IP (GDPR-friendly,
// matches the domain.IPHash fields).
func HashIP(ip string) string {
	h := sha256.Sum256([]byte("ignisforum-ip-salt:" + ip))
	return hex.EncodeToString(h[:8])
}

// HashEmail normalises + hashes an email for the lookup index.
func HashEmail(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(h[:])
}
