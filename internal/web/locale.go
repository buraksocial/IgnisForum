package web

import (
	"fmt"
	"time"
)

// uiLocale contains application copy, not forum content. Incomplete catalogs
// must only be selected explicitly on an isolated preview instance.
type uiLocale struct {
	Tag, Direction, ShortDate string
	messages                  map[string]string
	one                       func(int) bool
}

var turkish = &uiLocale{"tr", "ltr", "02.01.2006", map[string]string{
	"nav.forums": "Forumlar", "nav.latest": "Yeni içerikler", "nav.search": "Ara",
	"nav.notifications": "Bildirimler", "nav.admin": "Yönetim", "nav.home": "Ana Sayfa",
	"nav.login": "Giriş", "nav.register": "Kayıt", "nav.logout": "Çıkış",
	"theme.menu": "Tema", "theme.toggle": "Tema değiştir",
	"layout.rebuilding": "İçerik indeksleri yeniden oluşturuluyor — listeler geçici olarak eksik görünebilir.",
	"footer.source":     "açık kaynak", "footer.powered": "güç veren:",
	"home.title": "IgnisForum — Forum", "home.empty": "Henüz forum yok. Sunucuyu %s ile örnek veriyle başlatabilirsiniz.",
	"home.forum_counts": "%[1]s · %[2]s", "home.no_posts": "Henüz mesaj yok",
	"home.pinned": "Sabit Duyurular", "home.stats": "Forum İstatistikleri",
	"home.stats_threads": "Açılan konular", "home.stats_posts": "Yazılan mesajlar",
	"home.online": "Çevrimiçi Üyeler", "home.active": "Aktif Üyeler",
	"home.online_count.other": "Son 15 dakikada %d üye", "home.no_active": "Son 5 dakikada aktif üye yok.",
	"home.ad": "Reklam", "home.latest": "Son Konular", "home.no_threads": "Henüz konu yok.",
	"search.placeholder": "Konu ve mesajlarda ara…", "search.title": "Arama", "search.empty": "«%s» için sonuç bulunamadı.",
	"latest.empty": "Henüz içerik yok.", "latest.views": "görüntülenme", "latest.replies": "yanıt",
	"tag.title": "Etiket: %s", "tag.empty": "Bu etikette görüntülenebilir konu yok.", "tag.breadcrumb": "Etiketler",
	"notfound.title": "Bulunamadı", "notfound.heading": "404 — Bulunamadı", "notfound.body": "Aradığınız içerik kaldırılmış ya da hiç var olmamış olabilir.",
	"notfound.back":        "Ana sayfaya dön",
	"notifications.unread": "%d okunmamış", "notifications.reply": "%[1]s konuya yanıt verdi: %[2]s",
	"notifications.mark_read": "Okundu işaretle", "notifications.empty": "Henüz bildiriminiz yok.",
	"media.title": "Görsel önizleme", "media.external": "Harici görsel",
	"media.notice": "Bu görsel harici bir sunucudan yüklenir.", "media.source": "Kaynağı aç",
	"media.preview": "Görseli önizle", "media.download": "Harici indirme bağlantısı", "media.open": "Harici bağlantıyı aç",
	"attachment.download": "Dosyayı indir: %s", "attachment.remove": "Eki kaldır", "attachment.add": "Dosya ekle",
	"attachment.file": "Dosya (en fazla 512 KiB)", "attachment.upload": "Yükle",
	"count.threads.other": "%d konu", "count.posts.other": "%d mesaj", "count.replies.other": "%d yanıt",
	"time.now": "az önce", "time.minute.other": "%d dk önce", "time.hour.other": "%d sa önce", "time.day.other": "%d gün önce",
}, nil}

var englishPreview = &uiLocale{"en", "ltr", "2 Jan 2006", map[string]string{
	"nav.forums": "Forums", "nav.latest": "Latest activity", "nav.search": "Search",
	"nav.notifications": "Notifications", "nav.admin": "Administration", "nav.home": "Home",
	"nav.login": "Log in", "nav.register": "Register", "nav.logout": "Log out",
	"theme.menu": "Theme", "theme.toggle": "Switch color scheme",
	"layout.rebuilding": "Content indexes are being rebuilt. Some lists may be incomplete for now.",
	"footer.source":     "open source", "footer.powered": "powered by",
	"home.title": "IgnisForum — Forums", "home.empty": "There are no forums yet. Start the server with %s to load sample data.",
	"home.forum_counts": "%[1]s · %[2]s", "home.no_posts": "No posts yet",
	"home.pinned": "Pinned announcements", "home.stats": "Forum statistics",
	"home.stats_threads": "Threads started", "home.stats_posts": "Posts written",
	"home.online": "Members online", "home.active": "Active members",
	"home.online_count.one": "%d member in the last 15 minutes", "home.online_count.other": "%d members in the last 15 minutes",
	"home.no_active": "No members active in the last 5 minutes.",
	"home.ad":        "Advertisement", "home.latest": "Latest threads", "home.no_threads": "No threads yet.",
	"search.placeholder": "Search threads and posts…", "search.title": "Search", "search.empty": "No results for “%s”.",
	"latest.empty": "No content yet.", "latest.views": "views", "latest.replies": "replies",
	"tag.title": "Tag: %s", "tag.empty": "There are no visible threads with this tag.", "tag.breadcrumb": "Tags",
	"notfound.title": "Not found", "notfound.heading": "404 — Not found", "notfound.body": "The content might have been removed or never existed.",
	"notfound.back":        "Back to home",
	"notifications.unread": "%d unread", "notifications.reply": "%[1]s replied to: %[2]s",
	"notifications.mark_read": "Mark as read", "notifications.empty": "You have no notifications yet.",
	"media.title": "Image preview", "media.external": "External image",
	"media.notice": "This image loads from an external server.", "media.source": "Open source image",
	"media.preview": "Preview image", "media.download": "External download link", "media.open": "Open external link",
	"attachment.download": "Download file: %s", "attachment.remove": "Remove attachment", "attachment.add": "Attach a file",
	"attachment.file": "File (up to 512 KiB)", "attachment.upload": "Upload",
	"count.threads.one": "%d thread", "count.threads.other": "%d threads",
	"count.posts.one": "%d post", "count.posts.other": "%d posts",
	"count.replies.one": "%d reply", "count.replies.other": "%d replies",
	"time.now": "just now", "time.minute.one": "%d minute ago", "time.minute.other": "%d minutes ago",
	"time.hour.one": "%d hour ago", "time.hour.other": "%d hours ago", "time.day.one": "%d day ago", "time.day.other": "%d days ago",
}, func(n int) bool { return n == 1 }}

func (srv *Server) uiLocale() *uiLocale {
	if srv.D.PreviewLocale == "en" {
		return englishPreview
	}
	return turkish
}

func (l *uiLocale) Text(key string, args ...any) string {
	if l == nil {
		l = turkish
	}
	text, ok := l.messages[key]
	if !ok {
		text, ok = turkish.messages[key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

func (l *uiLocale) Count(key string, n int) string {
	if l != nil && l.one != nil && l.one(n) {
		if _, ok := l.messages[key+".one"]; ok {
			return l.Text(key+".one", n)
		}
	}
	return l.Text(key+".other", n)
}

func (l *uiLocale) Ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return l.Text("time.now")
	case d < time.Hour:
		return l.Count("time.minute", int(d/time.Minute))
	case d < 24*time.Hour:
		return l.Count("time.hour", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return l.Count("time.day", int(d/(24*time.Hour)))
	default:
		return t.Format(l.ShortDate)
	}
}
