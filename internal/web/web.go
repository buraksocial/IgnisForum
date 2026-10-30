// Package web wires chi routes to handlers that render SSR templates from the
// repo layer. Performance contract (§10): the home page is ONE Get (forum_tree)
// + one node-blob Get per forum + one windowed ZPage for latest threads; the
// thread view is one windowed ZOldest + batched JSON gets. No handler may
// enumerate a ZSET wholesale (use store.ZPage/ZOldest, never the ByScore
// materialisation path).
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// Version is stamped by main via ldflags.
var Version = "dev"

// Server holds shared dependencies for all handlers.
type Server struct {
	Store *store.Store
	Repos *repo.Repos
	css   []byte
	tmpl  *template.Template
}

// New builds the router with the M1 surface. `assets` is the embedded theme FS
// (templates/ + styles/thred/style.css) from the module root assets package.
func New(s *store.Store, r *repo.Repos, assets embed.FS) chi.Router {
	css, err := assets.ReadFile("styles/thred/style.css")
	if err != nil {
		panic("web: theme css missing from embed: " + err.Error())
	}
	srv := &Server{
		Store: s,
		Repos: r,
		css:   css,
		tmpl:  loadTemplates(assets),
	}

	rr := chi.NewRouter()
	rr.Use(chimw.Recoverer)
	rr.Use(chimw.RequestID)
	rr.Use(chimw.RealIP)

	rr.Get("/healthz", srv.healthz)
	rr.Get("/s/thred/style.css", srv.serveStyle)

	rr.Get("/", srv.home)
	rr.Get("/latest", srv.latest)
	rr.Get("/search", srv.search)
	rr.Get("/f/{nodeID}", srv.forum)
	rr.Get("/f/{nodeID}/new", srv.newThreadForm)
	rr.Post("/f/{nodeID}/new", srv.createThread)
	rr.Get("/t/{threadID}", srv.thread)
	rr.Get("/t/{threadID}/post/{postID}", srv.postRedirect)
	rr.Post("/t/{threadID}/reply", srv.reply)

	rr.NotFound(srv.notFound)
	return rr
}

// --- view models ---

type Crumb struct{ URL, Title string }

type pageData struct {
	Title      string
	Nav        string
	Crumbs     []Crumb
	Rebuilding bool
	Version    string
	Now        time.Time

	// home / latest
	Cats          []catView
	LatestThreads []threadRow

	// forum
	Node       *domain.Node
	Sort       string
	Page       int
	TotalPages int
	PageWindow []int
	Threads    []threadRow

	// thread
	Thread   *domain.Thread
	Posts    []postRow
	CanReply bool

	// search
	Query string
	Hits  []threadRow
	HaveQ bool
}

type catView struct {
	Title  string
	Forums []forumView
}

type forumView struct {
	ID            uint64
	Title         string
	Slug          string
	Description   string
	ThreadCount   int
	PostCount     int
	LastPostURL   string
	LastPostTitle string
	LastPoster    string
	LastPostTime  time.Time
}

type threadRow struct {
	ID            uint64
	Title         string
	Author        string
	Date          time.Time
	ReplyCount    int
	ViewCount     int
	LastPostID    uint64
	LastPostTitle string
	LastPoster    string
	LastPostTime  time.Time
}

type postRow struct {
	ID       uint64
	Number   int
	UserID   uint64
	Author   string
	Rank     string
	Date     time.Time
	Rendered template.HTML
	Edited   bool
}

// --- base helpers ---

func (srv *Server) base(title, nav string, crumbs []Crumb) *pageData {
	return &pageData{
		Title: title, Nav: nav, Crumbs: crumbs,
		Rebuilding: srv.Store.Rebuilding(),
		Version:    Version, Now: time.Now(),
	}
}

func (srv *Server) render(w http.ResponseWriter, name string, data *pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := srv.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
	}
}

func parseID(s string) uint64 { n, _ := strconv.ParseUint(s, 10, 64); return n }
func itoa(v uint64) string    { return strconv.FormatUint(v, 10) }

func parseInt(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

const pageSize = 20

func pageWindow(page, totalPages int) []int {
	if totalPages < 2 {
		return nil
	}
	lo, hi := page-3, page+3
	if lo < 1 {
		lo = 1
	}
	if hi > totalPages {
		hi = totalPages
	}
	out := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

func pageCount(total, size int) int {
	if total <= 0 {
		return 1
	}
	return (total + size - 1) / size
}

// --- infra handlers ---

func (srv *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	st := srv.Store.Stats()
	st["version"] = Version
	st["rebuilding"] = srv.Store.Rebuilding()
	b, _ := json.Marshal(st)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

func (srv *Server) serveStyle(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(srv.css)
}

func (srv *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	srv.render(w, "notfound", srv.base("Bulunamadı", "", nil))
}

// --- content handlers ---

func (srv *Server) home(w http.ResponseWriter, _ *http.Request) {
	tree, err := srv.Repos.Nodes.LoadTree()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d := srv.base("IgnisForum — Forum", "home", nil)
	d.Cats = srv.buildCats(tree)
	d.LatestThreads = srv.threadsFromIDs(srv.Store.ZPage(repo.IdxLatestThreads, 0, 10))
	srv.render(w, "home", d)
}

// buildCats turns the slim tree blob into a render model. One node-blob Get per
// forum supplies counts + last-post card; the engine hot cache keeps this cheap.
func (srv *Server) buildCats(tree *repo.Tree) []catView {
	out := make([]catView, 0, len(tree.Categories))
	for _, c := range tree.Categories {
		cv := catView{Title: c.Title}
		for _, f := range c.Forums {
			fv := forumView{ID: f.NodeID, Title: f.Title, Slug: f.Slug}
			if node, ok := srv.Repos.Nodes.Get(f.NodeID); ok {
				fv.Description = node.Description
				fv.ThreadCount = node.ThreadCount
				fv.PostCount = node.PostCount
				if node.LastPost != nil {
					fv.LastPostTime = node.LastPost.T
					fv.LastPoster = srv.username(node.LastPost.UserID)
					if post, ok := srv.Repos.Posts.Get(node.LastPost.PostID); ok {
						if th, ok := srv.Repos.Threads.Get(post.ThreadID); ok {
							fv.LastPostTitle = th.Title
							fv.LastPostURL = fmt.Sprintf("/t/%d/post/%d", th.ID, post.ID)
						}
					}
				}
			}
			cv.Forums = append(cv.Forums, fv)
		}
		out = append(out, cv)
	}
	return out
}

func (srv *Server) latest(w http.ResponseWriter, _ *http.Request) {
	d := srv.base("Yeni içerikler", "latest", nil)
	d.LatestThreads = srv.threadsFromIDs(srv.Store.ZPage(repo.IdxLatestThreads, 0, 50))
	srv.render(w, "latest", d)
}

func (srv *Server) forum(w http.ResponseWriter, r *http.Request) {
	nodeID := parseID(chi.URLParam(r, "nodeID"))
	node, ok := srv.Repos.Nodes.Get(nodeID)
	if !ok {
		srv.notFound(w, r)
		return
	}
	sortKey := r.URL.Query().Get("sort")
	switch sortKey {
	case "new", "posts", "views", "last", "":
	default:
		sortKey = "last"
	}
	page := parseInt(r, "p", 1)
	ids := srv.Repos.Threads.List(nodeID, sortKey, (page-1)*pageSize, pageSize)

	d := srv.base(node.Title, "home", []Crumb{{URL: "/f/" + itoa(nodeID), Title: node.Title}})
	d.Node = node
	d.Sort = sortKey
	d.Threads = srv.threadsFromIDs(ids)
	d.Page = page
	d.TotalPages = pageCount(srv.Store.ZCard(repo.NodeThreadsKey(nodeID)), pageSize)
	d.PageWindow = pageWindow(page, d.TotalPages)
	srv.render(w, "forum", d)
}

func (srv *Server) thread(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	th, ok := srv.Repos.Threads.Get(threadID)
	if !ok || th.Status == domain.ThreadDeleted {
		srv.notFound(w, r)
		return
	}
	// View counter: write primary + one in-node index score update (M1 accepts
	// the per-node O(n) shift; see §3 perf note).
	_ = srv.Repos.Threads.IncrementViews(threadID)

	page := parseInt(r, "p", 1)
	offset := (page - 1) * pageSize
	postIDs := srv.Repos.Posts.List(threadID, offset, pageSize)

	rows := make([]postRow, 0, len(postIDs))
	for i, pid := range postIDs {
		post, ok := srv.Repos.Posts.Get(pid)
		if !ok || post.State == 2 {
			continue
		}
		rows = append(rows, postRow{
			ID: post.ID, Number: offset + i + 1, UserID: post.UserID,
			Author: srv.username(post.UserID), Rank: "Üye", Date: post.Date,
			Rendered: template.HTML(post.MessageRendered),
			Edited:   post.EditCount > 0,
		})
	}

	d := srv.base(th.Title, "home", []Crumb{{URL: "/f/" + itoa(th.NodeID), Title: srv.nodeName(th.NodeID)}})
	d.Thread = th
	d.Posts = rows
	d.Page = page
	d.TotalPages = pageCount(srv.Store.ZCard(repo.ThreadPostsKey(threadID)), pageSize)
	d.PageWindow = pageWindow(page, d.TotalPages)
	d.CanReply = th.Status == domain.ThreadOpen
	srv.render(w, "thread", d)
}

// postRedirect resolves a /t/{tid}/post/{pid} deep link to the owning page by
// counting members at-or-before the post's score (bounded by thread size —
// ZCountUpTo materialises the range, which is fine for one thread's index).
func (srv *Server) postRedirect(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	postID := parseID(chi.URLParam(r, "postID"))
	post, ok := srv.Repos.Posts.Get(postID)
	if !ok || post.ThreadID != threadID {
		srv.notFound(w, r)
		return
	}
	rank := srv.Store.ZCountUpTo(repo.ThreadPostsKey(threadID), float64(post.Date.Unix()))
	page := (rank-1)/pageSize + 1
	if page < 1 {
		page = 1
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%s?p=%d#post-%s", itoa(threadID), page, itoa(postID)), http.StatusFound)
}

func (srv *Server) reply(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	if err := r.ParseForm(); err != nil {
		srv.notFound(w, r)
		return
	}
	msg := strings.TrimSpace(r.FormValue("message"))
	if msg == "" {
		http.Redirect(w, r, "/t/"+itoa(threadID), http.StatusSeeOther)
		return
	}
	post, err := srv.Repos.Posts.Create(threadID, 1, msg) // M2: real session user
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rank := srv.Store.ZCountUpTo(repo.ThreadPostsKey(threadID), float64(post.Date.Unix()))
	page := (rank-1)/pageSize + 1
	http.Redirect(w, r, fmt.Sprintf("/t/%s?p=%d#post-%s", itoa(threadID), page, itoa(post.ID)), http.StatusSeeOther)
}

func (srv *Server) newThreadForm(w http.ResponseWriter, r *http.Request) {
	nodeID := parseID(chi.URLParam(r, "nodeID"))
	node, ok := srv.Repos.Nodes.Get(nodeID)
	if !ok {
		srv.notFound(w, r)
		return
	}
	d := srv.base("Yeni Konu", "home", []Crumb{
		{URL: "/f/" + itoa(nodeID), Title: node.Title}, {Title: "Yeni Konu"},
	})
	d.Node = node
	srv.render(w, "newthread", d)
}

func (srv *Server) createThread(w http.ResponseWriter, r *http.Request) {
	nodeID := parseID(chi.URLParam(r, "nodeID"))
	if err := r.ParseForm(); err != nil {
		srv.notFound(w, r)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	msg := strings.TrimSpace(r.FormValue("message"))
	if title == "" || msg == "" {
		http.Error(w, "başlık ve mesaj gerekli", http.StatusBadRequest)
		return
	}
	th, _, err := srv.Repos.Threads.Create(nodeID, 1, title, msg) // M2: session uid
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/t/"+itoa(th.ID), http.StatusSeeOther)
}

func (srv *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d := srv.base("Arama", "search", nil)
	d.Query = q
	d.HaveQ = q != ""
	if q != "" {
		hits := srv.Store.FTSearch(q, 50)
		seen := make(map[uint64]struct{}, len(hits))
		threads := make([]uint64, 0, len(hits))
		for _, h := range hits {
			if !strings.HasPrefix(h.DocID, "post:") {
				continue
			}
			pid := parseID(strings.TrimPrefix(h.DocID, "post:"))
			post, ok := srv.Repos.Posts.Get(pid)
			if !ok || post.State != 0 {
				continue
			}
			if _, dup := seen[post.ThreadID]; dup {
				continue
			}
			seen[post.ThreadID] = struct{}{}
			threads = append(threads, post.ThreadID)
		}
		d.Hits = srv.threadsFromIDs(threads)
	}
	srv.render(w, "search", d)
}

// --- shared view helpers ---

func (srv *Server) threadsFromIDs(ids []uint64) []threadRow {
	rows := make([]threadRow, 0, len(ids))
	for _, id := range ids {
		th, ok := srv.Repos.Threads.Get(id)
		if !ok || th.Status == domain.ThreadDeleted {
			continue
		}
		replies := th.PostCount - 1
		if replies < 0 {
			replies = 0
		}
		row := threadRow{
			ID: th.ID, Title: th.Title, Date: th.Date,
			ReplyCount: replies, ViewCount: th.ViewCount,
			Author: srv.username(th.UserID),
		}
		if th.LastPost != nil {
			row.LastPostID = th.LastPost.PostID
			row.LastPostTime = th.LastPost.T
			row.LastPoster = srv.username(th.LastPost.UserID)
			row.LastPostTitle = th.Title
		}
		rows = append(rows, row)
	}
	return rows
}

func (srv *Server) username(uid uint64) string {
	// M2 replaces this with real user blobs; demo names until then.
	switch uid {
	case 1:
		return "admin"
	case 2:
		return "buraks"
	case 3:
		return "zerap"
	}
	return "kullanıcı #" + itoa(uid)
}

func (srv *Server) nodeName(id uint64) string {
	if n, ok := srv.Repos.Nodes.Get(id); ok {
		return n.Title
	}
	return "Forum"
}

// --- templates ---

func loadTemplates(assets embed.FS) *template.Template {
	entries, err := assets.ReadDir("templates")
	if err != nil {
		panic("templates dir missing from embed: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tmpl") {
			names = append(names, "templates/"+e.Name())
		}
	}
	sort.Strings(names)
	t := template.New("").Funcs(tmplFuncs)
	return template.Must(t.ParseFS(assets, names...))
}

var tmplFuncs = template.FuncMap{
	"ago": humanTime,
	"first": func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return "?"
		}
		r := []rune(s)
		return strings.ToUpper(string(r[0]))
	},
	"sub": func(a, b int) int { return a - b },
	"add": func(a, b int) int { return a + b },
}

func humanTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "az önce"
	case d < time.Hour:
		return fmt.Sprintf("%d dk önce", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d sa önce", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d gün önce", int(d.Hours()/24))
	default:
		return t.Format("02.01.2006")
	}
}
