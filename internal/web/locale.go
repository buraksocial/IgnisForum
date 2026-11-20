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
	"nav.forums": "Forumlar", "nav.latest": "Yeni içerikler", "nav.search": "Ara", "nav.menu": "Menüyü aç",
	"nav.notifications": "Bildirimler", "nav.admin": "Yönetim", "nav.home": "Ana Sayfa",
	"nav.login": "Giriş", "nav.register": "Kayıt", "nav.logout": "Çıkış",
	"admin.home": "Yönetim merkezi", "admin.back": "Yönetime dön", "admin.appearance": "Görünüm ve eklentiler", "admin.manage_appearance": "Temaları ve eklentileri yönet",
	"admin.structure": "Forum yapısı", "admin.structure_help": "Kategori ve forumları ekleyin, düzenleyin veya boş düğümleri kaldırın.", "admin.manage_nodes": "Forumları yönet",
	"admin.queue": "Rapor kuyruğu", "admin.queue_help": "Üyelerin bildirdiği mesajları inceleyin ve sonuçlandırın.", "admin.review": "Raporları incele",
	"admin.permissions": "İzinler", "admin.permissions_help": "Misafir, üye ve yönetici grupları için genel veya forum bazlı erişimleri yönetin.", "admin.matrix": "İzin matrisi", "admin.open_matrix": "İzin matrisini aç",
	"admin.security": "Güvenlik", "admin.security_help": "Yönetim değişiklikleri oturum, CSRF doğrulaması ve işlem sınırıyla korunur.", "admin.denied": "Bu işlem için yönetici izni gerekiyor.",
	"report.heading": "Moderasyon raporları", "report.number": "Rapor #%s", "report.go": "Mesaja git", "report.resolve": "Çözüldü", "report.empty": "Açık rapor yok.", "report.missing": "Rapor bulunamadı.",
	"permission.scope": "Kapsam", "permission.global": "Genel", "permission.node_scope": "Kategori / forum", "permission.node": "Düğüm", "permission.open": "Kapsamı aç", "permission.permission": "İzin",
	"permission.inherit": "Devral", "permission.allow": "İzin ver", "permission.deny": "Reddet", "permission.save": "İzinleri kaydet",
	"group.guest": "Misafir", "group.member": "Üye", "group.admin": "Yönetici",
	"node.category": "Kategori", "node.forum": "Forum",
	"node.bad_fields": "Başlık, slug veya açıklama geçersiz.", "node.category_failed": "Kategori oluşturulamadı.", "node.bad_parent": "Üst kategori geçersiz.", "node.forum_failed": "Forum oluşturulamadı.", "node.bad_id": "Düğüm kimliği geçersiz.", "node.update_failed": "Düğüm güncellenemedi.", "node.move_failed": "Düğüm taşınamadı.", "node.not_empty": "Yalnızca boş forum veya kategoriler silinebilir.", "node.bad_action": "İşlem geçersiz.",
	"node.create_category": "Kategori oluştur", "node.create_forum": "Forum oluştur", "node.title": "Başlık", "node.slug": "Slug", "node.description": "Açıklama", "node.parent": "Üst kategori", "node.allow_polls": "Anketlere izin ver",
	"node.add_category": "Kategori ekle", "node.add_forum": "Forum ekle", "node.existing": "Mevcut düğümler", "node.delete": "Boşsa sil", "node.confirm": "Yalnızca boş düğüm silinir. Devam edilsin mi?", "node.empty": "Henüz kategori veya forum yok.",
	"permission.access": "Siteye erişim", "permission.view": "Forumları görüntüleme", "permission.thread": "Konu açma", "permission.reply": "Yanıt yazma", "permission.search": "Arama", "permission.register": "Kayıt olma", "permission.admin": "Yönetim paneli",
	"thread.back": "Foruma dön", "thread.follow": "Konuyu takip et", "thread.unfollow": "Takip ediliyor · Takibi bırak",
	"thread.counts": "%[1]s · %[2]s", "count.views.other": "%d görüntülenme", "thread.member": "Üye", "thread.guest": "Misafir", "thread.user": "kullanıcı #%s",
	"post.edited": "Son düzenleme: %[1]s · %[2]s", "post.permalink": "Kalıcı bağlantı", "post.quote": "Alıntıla", "post.edit": "Düzenle", "post.save": "Değişiklikleri kaydet",
	"post.delete": "Sil", "post.delete_reply": "Bu yanıtı sil", "post.report": "Raporla", "post.reason": "Gerekçe", "post.send_report": "Moderatörlere gönder", "post.reactions": "Mesaj tepkileri",
	"thread.empty": "Bu konuda henüz mesaj yok.", "thread.quick_reply": "Hızlı Yanıt", "thread.reply_hint": "Cevabınızı yazın… ([b]kalın[/b], [quote]alıntı[/quote], [code]kod[/code])", "thread.reply": "Yanıtla", "thread.login_reply": "Yanıt yazmak için giriş yapın.",
	"newthread.heading": "Yeni Konu — %s", "newthread.title": "Başlık", "newthread.title_hint": "Konu başlığı", "newthread.message": "Mesaj", "newthread.message_hint": "[b]kalın[/b], [url=https://example.com]bağlantı[/url], [code]kod[/code] desteklenir",
	"newthread.tags": "Etiketler (virgülle ayırın, en fazla 5)", "newthread.tags_hint": "go, performans, soru", "newthread.submit": "Konuyu Aç", "newthread.cancel": "İptal",
	"poll.vote": "Oy ver", "poll.saved": "Oyunuz kaydedildi.", "poll.login": "Oy vermek için giriş yapın.", "poll.selected": "Seçiminiz", "poll.optional": "Anket (isteğe bağlı)", "poll.question": "Soru", "poll.options": "Seçenekler (her satıra bir seçenek, 2-10)", "poll.multiple": "Birden fazla seçenek işaretlenebilsin",
	"reaction.like": "Beğen", "reaction.love": "Sev", "reaction.laugh": "Komik", "reaction.wow": "Şaşırtıcı", "reaction.sad": "Üzücü",
	"auth.login": "Giriş Yap", "auth.register": "Kayıt Ol", "auth.create": "Hesap Oluştur",
	"auth.identity": "Kullanıcı adı veya e-posta", "auth.username": "Kullanıcı adı", "auth.email": "E-posta", "auth.password": "Şifre",
	"auth.remember": "Beni hatırla", "auth.username_hint": "En az 2 karakter, harf ile başlayın", "auth.password_hint": "En az 8 karakter",
	"auth.bad_form": "İstek çözümlenemedi.", "auth.bad_csrf": "Oturum doğrulaması başarısız (CSRF). Formu yenileyip tekrar deneyin.",
	"auth.bad_credentials": "Geçersiz kullanıcı adı veya şifre.", "auth.closed": "Kayıt şu anda kapalı.",
	"auth.username_taken": "Bu kullanıcı adı zaten alınmış.", "auth.email_taken": "Bu e-posta ile başka bir hesap var.",
	"auth.bad_username": "Kullanıcı adı geçersiz.", "auth.bad_email": "E-posta geçersiz.", "auth.short_password": "Şifre en az 8 karakter olmalı.",
	"auth.session_failed": "Oturum kurulamadı.", "auth.register_failed": "Hesap oluşturulamadı.", "auth.csrf_unavailable": "Form şu anda açılamıyor. Lütfen tekrar deneyin.",
	"error.heading": "Bu işlem geçersiz", "error.back": "Geri dön", "error.throttled": "Çok fazla deneme",
	"error.retry.other": "Çok fazla deneme yaptınız. Lütfen %d saniye sonra tekrar deneyin.",
	"forum.new":         "Yeni Konu", "forum.last": "Son mesaj", "forum.started": "Konu başlangıcı", "forum.replies": "Yanıt sayısı", "forum.views": "Görüntülenme",
	"forum.empty": "Bu forumda henüz konu yok.", "forum.denied": "Bu forumu görüntüleme izniniz yok.",
	"profile.joined": "Üyelik tarihi", "profile.posts": "Mesaj", "profile.likes": "Beğeni puanı", "profile.edit": "Profili düzenle",
	"profile.title": "Üye başlığı", "profile.signature": "İmza", "profile.save": "Kaydet", "profile.recent": "Son mesajlar", "profile.empty": "Görüntülenebilir mesaj yok.",
	"profile.denied": "Yalnızca kendi profilinizi düzenleyebilirsiniz.", "profile.too_long": "Başlık en fazla 60, imza en fazla 500 karakter olabilir.", "profile.failed": "Profil kaydedilemedi.",
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
	"nav.forums": "Forums", "nav.latest": "Latest activity", "nav.search": "Search", "nav.menu": "Open menu",
	"nav.notifications": "Notifications", "nav.admin": "Administration", "nav.home": "Home",
	"nav.login": "Log in", "nav.register": "Register", "nav.logout": "Log out",
	"admin.home": "Administration center", "admin.back": "Back to administration", "admin.appearance": "Appearance and plugins", "admin.manage_appearance": "Manage themes and plugins",
	"admin.structure": "Forum structure", "admin.structure_help": "Add or edit categories and forums, or remove empty nodes.", "admin.manage_nodes": "Manage forums",
	"admin.queue": "Report queue", "admin.queue_help": "Review and resolve posts reported by members.", "admin.review": "Review reports",
	"admin.permissions": "Permissions", "admin.permissions_help": "Manage global and forum access for guests, members and administrators.", "admin.matrix": "Permission matrix", "admin.open_matrix": "Open permission matrix",
	"admin.security": "Security", "admin.security_help": "Administration changes require a session, CSRF verification and rate limiting.", "admin.denied": "This action requires administrator permission.",
	"report.heading": "Moderation reports", "report.number": "Report #%s", "report.go": "Go to post", "report.resolve": "Resolve", "report.empty": "There are no open reports.", "report.missing": "Report not found.",
	"permission.scope": "Scope", "permission.global": "Global", "permission.node_scope": "Category / forum", "permission.node": "Node", "permission.open": "Open scope", "permission.permission": "Permission",
	"permission.inherit": "Inherit", "permission.allow": "Allow", "permission.deny": "Deny", "permission.save": "Save permissions",
	"group.guest": "Guest", "group.member": "Member", "group.admin": "Administrator",
	"node.category": "Category", "node.forum": "Forum",
	"node.bad_fields": "Invalid title, slug or description.", "node.category_failed": "The category could not be created.", "node.bad_parent": "Invalid parent category.", "node.forum_failed": "The forum could not be created.", "node.bad_id": "Invalid node ID.", "node.update_failed": "The node could not be updated.", "node.move_failed": "The node could not be moved.", "node.not_empty": "Only empty forums or categories can be deleted.", "node.bad_action": "Invalid action.",
	"node.create_category": "Create category", "node.create_forum": "Create forum", "node.title": "Title", "node.slug": "Slug", "node.description": "Description", "node.parent": "Parent category", "node.allow_polls": "Allow polls",
	"node.add_category": "Add category", "node.add_forum": "Add forum", "node.existing": "Existing nodes", "node.delete": "Delete if empty", "node.confirm": "Only empty nodes can be deleted. Continue?", "node.empty": "There are no categories or forums yet.",
	"permission.access": "Access site", "permission.view": "View forums", "permission.thread": "Create threads", "permission.reply": "Write replies", "permission.search": "Search", "permission.register": "Register", "permission.admin": "Administration panel",
	"thread.back": "Back to forum", "thread.follow": "Follow thread", "thread.unfollow": "Following · Unfollow",
	"thread.counts": "%[1]s · %[2]s", "count.views.one": "%d view", "count.views.other": "%d views", "thread.member": "Member", "thread.guest": "Guest", "thread.user": "user #%s",
	"post.edited": "Last edited: %[1]s · %[2]s", "post.permalink": "Permalink", "post.quote": "Quote", "post.edit": "Edit", "post.save": "Save changes",
	"post.delete": "Delete", "post.delete_reply": "Delete this reply", "post.report": "Report", "post.reason": "Reason", "post.send_report": "Send to moderators", "post.reactions": "Post reactions",
	"thread.empty": "There are no posts in this thread yet.", "thread.quick_reply": "Quick reply", "thread.reply_hint": "Write your reply… ([b]bold[/b], [quote]quote[/quote], [code]code[/code])", "thread.reply": "Reply", "thread.login_reply": "Log in to reply.",
	"newthread.heading": "New thread — %s", "newthread.title": "Title", "newthread.title_hint": "Thread title", "newthread.message": "Message", "newthread.message_hint": "Supports [b]bold[/b], [url=https://example.com]links[/url] and [code]code[/code]",
	"newthread.tags": "Tags (separate with commas, up to 5)", "newthread.tags_hint": "go, performance, question", "newthread.submit": "Create thread", "newthread.cancel": "Cancel",
	"poll.vote": "Vote", "poll.saved": "Your vote has been recorded.", "poll.login": "Log in to vote.", "poll.selected": "Your choice", "poll.optional": "Poll (optional)", "poll.question": "Question", "poll.options": "Options (one per line, 2–10)", "poll.multiple": "Allow multiple choices",
	"reaction.like": "Like", "reaction.love": "Love", "reaction.laugh": "Funny", "reaction.wow": "Wow", "reaction.sad": "Sad",
	"auth.login": "Log in", "auth.register": "Register", "auth.create": "Create account",
	"auth.identity": "Username or email", "auth.username": "Username", "auth.email": "Email", "auth.password": "Password",
	"auth.remember": "Remember me", "auth.username_hint": "Start with a letter and use at least 2 characters", "auth.password_hint": "At least 8 characters",
	"auth.bad_form": "The request could not be read.", "auth.bad_csrf": "The form could not be verified. Reload it and try again.",
	"auth.bad_credentials": "Invalid username or password.", "auth.closed": "Registration is currently closed.",
	"auth.username_taken": "That username is already taken.", "auth.email_taken": "An account already uses that email address.",
	"auth.bad_username": "Invalid username.", "auth.bad_email": "Invalid email address.", "auth.short_password": "Use a password of at least 8 characters.",
	"auth.session_failed": "The session could not be started.", "auth.register_failed": "The account could not be created.", "auth.csrf_unavailable": "The form is unavailable. Please try again.",
	"error.heading": "This action could not be completed", "error.back": "Go back", "error.throttled": "Too many attempts",
	"error.retry.one": "Too many attempts. Please try again in %d second.", "error.retry.other": "Too many attempts. Please try again in %d seconds.",
	"forum.new": "New thread", "forum.last": "Last post", "forum.started": "Date started", "forum.replies": "Reply count", "forum.views": "Views",
	"forum.empty": "There are no threads in this forum yet.", "forum.denied": "You do not have permission to view this forum.",
	"profile.joined": "Joined", "profile.posts": "Posts", "profile.likes": "Like score", "profile.edit": "Edit profile",
	"profile.title": "Member title", "profile.signature": "Signature", "profile.save": "Save", "profile.recent": "Recent posts", "profile.empty": "There are no visible posts.",
	"profile.denied": "You can only edit your own profile.", "profile.too_long": "The title may contain up to 60 characters and the signature up to 500.", "profile.failed": "The profile could not be saved.",
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
