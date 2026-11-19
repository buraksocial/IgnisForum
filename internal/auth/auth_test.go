package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/buraksocial/IgnisForum/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(store.OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPasswordHash covers argon2id round-trip + negative cases.
func TestPasswordHash(t *testing.T) {
	a := NewService(openStore(t))
	h, err := a.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=4$") {
		t.Errorf("hash format = %q", h)
	}
	if !a.VerifyPassword("correct-horse-battery", h) {
		t.Error("verify of correct password failed")
	}
	if a.VerifyPassword("wrong", h) {
		t.Error("verify accepted a wrong password")
	}
	// Corrupted encodings must reject, not panic.
	for _, bad := range []string{"", "$argon2id", "plain", "$argon2id$v=19$m=x$y$z", strings.Repeat("$", 5)} {
		if a.VerifyPassword("whatever", bad) {
			t.Errorf("verify accepted malformed %q", bad)
		}
	}
}

// TestSessionLifecycle covers create/get/CSRF/destroy + expiry.
func TestSessionLifecycle(t *testing.T) {
	s := openStore(t)
	a := NewService(s)

	sess, err := a.CreateSession(7, false, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.GetSession(sess.SID); got == nil || got.UserID != 7 || got.CSRF != sess.CSRF {
		t.Fatalf("GetSession = %+v", got)
	}
	if !ValidateCSRF(sess, sess.CSRF) || ValidateCSRF(sess, "tampered") || ValidateCSRF(nil, sess.CSRF) {
		t.Error("CSRF validation wrong")
	}

	// Unknown sid.
	if a.GetSession(strings.Repeat("f", 64)) != nil {
		t.Error("unknown sid returned a session")
	}

	// Expired sessions: lazy-deleted on read.
	exp := *sess
	exp.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := s.PutJSON("session:"+sess.SID, &exp); err != nil {
		t.Fatal(err)
	}
	if a.GetSession(sess.SID) != nil {
		t.Error("expired session returned")
	}
	if ok, _ := s.GetJSON("session:"+sess.SID, &Session{}); ok {
		t.Error("expired session was not lazily deleted")
	}

	// Destroy.
	sess2, _ := a.CreateSession(8, true, "198.51.100.1")
	a.DestroySession(sess2.SID)
	if a.GetSession(sess2.SID) != nil {
		t.Error("session survived destroy")
	}

	// Remember vs short TTL.
	if s1, _ := a.CreateSession(9, false, "ip"); !s1.ExpiresAt.After(time.Now().Add(11 * time.Hour)) {
		t.Error("short session TTL wrong")
	}
	if s2, _ := a.CreateSession(9, true, "ip"); !s2.ExpiresAt.After(time.Now().Add(13 * 24 * time.Hour)) {
		t.Error("remember TTL wrong")
	}
}

// TestRegisterLogin walks the happy path + every uniqueness/policy error.
func TestRegisterLogin(t *testing.T) {
	s := openStore(t)
	a := NewService(s)
	rl := NewRateLimiter(s)
	u := NewUserRepo(a, rl)

	usr, err := u.Register("buraks", "buraks@example.com", "supersecret1")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if usr.ID == 0 || usr.PasswordHash == "" || len(usr.GroupIDs) == 0 {
		t.Fatalf("user blob incomplete: %+v", usr)
	}

	// Duplicate username (case-insensitive) + duplicate email.
	if _, err := u.Register("BuRaKs", "other@example.com", "supersecret1"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("dup username err = %v, want ErrUsernameTaken", err)
	}
	if _, err := u.Register("fresh", "buraks@example.com", "supersecret1"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("dup email err = %v, want ErrEmailTaken", err)
	}
	// Policy.
	if _, err := u.Register("a", "x@example.com", "supersecret1"); !errors.Is(err, ErrBadUsername) {
		t.Errorf("short username err = %v", err)
	}
	if _, err := u.Register("kötü*isim", "x@example.com", "supersecret1"); !errors.Is(err, ErrBadUsername) {
		t.Errorf("bad chars err = %v", err)
	}
	if _, err := u.Register("good", "not-an-email", "supersecret1"); !errors.Is(err, ErrBadEmail) {
		t.Errorf("bad email err = %v", err)
	}
	if _, err := u.Register("good2", "g@example.com", "short"); !errors.Is(err, ErrShortPassword) {
		t.Errorf("short pw err = %v", err)
	}

	// Turkish username is valid (unicode letters).
	if _, err := u.Register("ümit_ö", "umit@example.com", "supersecret1"); err != nil {
		t.Errorf("unicode username: %v", err)
	}

	// Login by username, by email (normalised), and by mixed case.
	for _, id := range []string{"buraks", "BURAKS", "buraks@example.com", "  Buraks@Example.COM "} {
		got, sess, err := u.Login(id, "supersecret1", false, "1.2.3.4")
		if err != nil {
			t.Fatalf("login %q: %v", id, err)
		}
		if got.Username != "buraks" || sess == nil || sess.UserID != got.ID {
			t.Fatalf("login %q returned %+v sess=%+v", id, got, sess)
		}
	}

	// Wrong password → unified error.
	if _, _, err := u.Login("buraks", "nope", false, "1.2.3.4"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong pw err = %v", err)
	}
	// Unknown user → the SAME error value.
	if _, _, err := u.Login("ghost", "nope", false, "1.2.3.4"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown user err = %v", err)
	}

	// Banned account (State != 0).
	usr.State = 1
	if err := s.PutJSON("user:"+itoaA(usr.ID), usr); err != nil {
		t.Fatal(err)
	}
	if _, _, err := u.Login("buraks", "supersecret1", false, "1.2.3.4"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("banned login must be unified, err = %v", err)
	}

	// Username() lookup + miss.
	if u.Username(usr.ID) != "buraks" {
		t.Errorf("Username = %q", u.Username(usr.ID))
	}
	if u.Username(999) != "" {
		t.Error("Username(unknown) must be empty")
	}
}

func TestAdminPasswordRecovery(t *testing.T) {
	s := openStore(t)
	a := NewService(s)
	u := NewUserRepo(a, NewRateLimiter(s))
	oldHash, err := a.HashPassword("known-insecure-demo-password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := u.CreateUserDirect(1, "admin", "admin@example.com", oldHash, []uint64{GroupAdmin}, 1)
	if err != nil {
		t.Fatal(err)
	}
	admin.State = 1
	if err := s.PutJSON("user:1", admin); err != nil {
		t.Fatal(err)
	}
	if err := u.SetPassword(1, "a-unique-admin-password-2026"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := u.Login("admin", "a-unique-admin-password-2026", false, "127.0.0.1"); err != nil {
		t.Fatalf("recovered admin login: %v", err)
	}
	if err := u.SetPassword(1, "short"); err == nil {
		t.Fatal("accepted a short recovery password")
	}
}

func itoaA(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// TestRateLimiter covers the sliding window, per-bucket isolation, Retry-After
// honesty, and Clear-on-success.
func TestRateLimiter(t *testing.T) {
	s := openStore(t)
	rl := NewRateLimiter(s)

	// 3 / 1h for action "login", bucket "alice".
	for i := 0; i < 3; i++ {
		if ok, _ := rl.Allow("login", "alice", 3, time.Hour); !ok {
			t.Fatalf("event %d rejected, want allowed", i)
		}
	}
	ok, retry := rl.Allow("login", "alice", 3, time.Hour)
	if ok {
		t.Fatal("4th event must be rejected")
	}
	if retry <= 0 || retry > time.Hour {
		t.Errorf("Retry-After = %v, want within the window", retry)
	}

	// Other buckets are unaffected.
	if ok, _ := rl.Allow("login", "bob", 3, time.Hour); !ok {
		t.Error("bob bucket must be independent")
	}
	if ok, _ := rl.Allow("register", "alice", 3, time.Hour); !ok {
		t.Error("register action must be independent of login")
	}

	// Clear resets.
	rl.Clear("login", "alice")
	if ok, _ := rl.Allow("login", "alice", 3, time.Hour); !ok {
		t.Error("after Clear the bucket must allow again")
	}

	// A tiny window expires: 1 per 30ms → the second call inside 30ms rejects,
	// after waiting it allows again.
	if ok, _ := rl.Allow("sms", "x", 1, 30*time.Millisecond); !ok {
		t.Fatal("first sms must pass")
	}
	if ok, _ := rl.Allow("sms", "x", 1, 30*time.Millisecond); ok {
		t.Error("second sms inside window must be rejected")
	}
	time.Sleep(60 * time.Millisecond)
	if ok, _ := rl.Allow("sms", "x", 1, 30*time.Millisecond); !ok {
		t.Error("sms must pass again after the window rolls")
	}
}

// TestHashHelpers asserts stability + normalisation of the one-way helpers.
func TestHashHelpers(t *testing.T) {
	if HashEmail("  AliKasa@Example.com ") != HashEmail("alikasa@example.com") {
		t.Error("email hash must normalise case + whitespace")
	}
	if HashEmail("a@b.co") == HashEmail("x@y.co") {
		t.Error("email collision")
	}
	h1, h2 := HashIP("1.2.3.4"), HashIP("5.6.7.8")
	if h1 == "" || h1 == h2 || len(h1) != 16 {
		t.Errorf("ip hash = %q %q", h1, h2)
	}
}
