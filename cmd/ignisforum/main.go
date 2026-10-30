// Command ignisforum is the IgnisForum server binary: it embeds the IgnisDB-Go
// LSM engine in-process (no network hop), serves SSR pages over chi, and owns
// the derived-index rebuild discipline.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	assets "github.com/buraksocial/IgnisForum"
	"github.com/buraksocial/IgnisForum/internal/config"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
	"github.com/buraksocial/IgnisForum/internal/web"
)

var (
	version   = "dev" // stamped at build time via -ldflags
	buildTime = "none"
)

func main() {
	cfgPath := flag.String("config", "/etc/ignisforum/config.yaml", "path to config yaml")
	listen := flag.String("listen", "", "override listen address (env IGNISFORUM_LISTEN)")
	dataDir := flag.String("data", "", "override data dir (env IGNISFORUM_DATA_DIR)")
	seed := flag.Bool("seed", false, "seed demo content (idempotent) and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("ignisforum %s (built %s)\n", version, buildTime)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *listen != "" {
		cfg.ListenAddr = *listen
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	st, err := store.Open(store.OpenConfig{
		DataDir:             cfg.DataDir,
		MemtableSize:        cfg.MemtableSize,
		CacheSize:           cfg.CacheSize,
		CompactionThreshold: cfg.CompactionThreshold,
	})
	if err != nil {
		log.Fatalf("store open: %v", err)
	}
	stopHeartbeat := store.StartHeartbeat(cfg.DataDir)
	defer stopHeartbeat()

	stats := st.Stats()
	log.Printf("IgnisForum %s (built %s) engine ready: data=%s rust_core_active=%v cache=%dMB",
		version, buildTime, cfg.DataDir, stats["rust_core_active"], cfg.CacheSize>>20)

	r := repo.New(st)

	// Startup rebuild of derived indexes: ZSETs + FT live in RAM only (§3.4), so
	// every boot re-derives them from primary data. It is O(live data) and runs
	// before traffic is served (Rebuilding() would just show a banner).
	rep := st.RebuildContent(r.Hooks)
	log.Printf("rebuild: %d scanned, %d recovered, %d cleared, %.3fs",
		rep.Scanned, rep.Recovered, rep.Cleared, rep.Elapsed)
	// Consistency pass: a crash between a node write and its tree update leaves
	// forum_tree short; re-materialise it from the re-derived children index.
	if err := r.Nodes.BuildTree(); err != nil {
		log.Fatalf("build tree: %v", err)
	}

	if *seed {
		if err := seedDemo(r); err != nil {
			log.Fatalf("seed: %v", err)
		}
		st.Flush()
		if err := st.Close(); err != nil {
			log.Fatalf("store close: %v", err)
		}
		return
	}

	web.Version = version
	rtr := web.New(st, r, assets.FS)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           rtr,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down…")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	st.Flush()
	if err := st.Close(); err != nil {
		log.Printf("store close: %v", err)
	}
	log.Printf("bye")
}

// seedDemo creates the demo structure (idempotent: skips when forums exist).
// It exercises the real repo write path — primaries, indexes, counters, FT — so
// the seeded site is exactly what production traffic would produce.
func seedDemo(r *repo.Repos) error {
	tree, err := r.Nodes.LoadTree()
	if err != nil {
		return err
	}
	if len(tree.Categories) > 0 {
		log.Printf("seed: data dir already seeded, skipping")
		return nil
	}

	genel, err := r.Nodes.CreateCategory("Genel", "genel", "Siteye özgü genel bölümler")
	if err != nil {
		return err
	}
	teknik, err := r.Nodes.CreateCategory("Teknik", "teknik", "Altyapı, performans ve kod")
	if err != nil {
		return err
	}
	tanis, err := r.Nodes.CreateForum(genel.ID, "Tanışma", "tanisma", "Yeni gelenler burada kendini tanıtır.", false)
	if err != nil {
		return err
	}
	sohbet, err := r.Nodes.CreateForum(genel.ID, "Sohbet", "sohbet", "Her konuda serbest sohbet.", false)
	if err != nil {
		return err
	}
	prog, err := r.Nodes.CreateForum(teknik.ID, "Go Programlama", "go-programlama", "Go dilinde sorular, kod incelemeleri, pratik.", true)
	if err != nil {
		return err
	}
	altyapi, err := r.Nodes.CreateForum(teknik.ID, "Sunucu & Altyapı", "sunucu-altyapi", "LSM motoru, nginx/Plesk, performans ölçümü.", false)
	if err != nil {
		return err
	}

	type st struct {
		node    uint64
		user    uint64
		title   string
		msg     string
		replies []string
	}
	threads := []st{
		{
			node: tanis.ID, user: 2,
			title: "Merhaba, burası yeni forumumuz",
			msg:   "[b]Selam arkadaşlar![/b]\n\nFlarum'dan göç ettik; altyapı artık [url=https://github.com/buraksocial/IgnisDB-Go]IgnisDB-Go[/url] LSM motorunun üzerine yazılmış, tek binary'li bir Go sunucu. İlk izlenimler: [i]hız ve sadelik[/i] :)\n\nSizin yorumlarınızı duymak isterim.",
			replies: []string{
				"[quote]ilk izlenimler: hız ve sadelik[/quote]\nBen de aynı fikirdeyim. TTFB'ler çok düşük görünüyor; benchmark paylaşır mısınız?",
				"Kutlamalı! [b]İlk post benim olsun[/b] :)",
			},
		},
		{
			node: sohbet.ID, user: 3,
			title: "İgnisForum neden Go ile yazıldı?",
			msg:   "Kısa cevap: [b]tek binary, gömülü veritabanı, deterministik gecikme[/b].\n\nAyrı bir MySQL servisi yok; motor süreç-içinde çalışıyor. Gömülü mimarinin forum ölçeğinde (ayda birkaç milyon mesaj) yeterlidir ve operasyon maliyetini sıfırlar.",
			replies: []string{
				"PG/MySQL sevmememe rağmen burada haklısınız. Gömülü motor + LSM, read-heavy forum iş yükü için mantıklı.",
			},
		},
		{
			node: prog.ID, user: 2,
			title: "ZSET sayfalama: ZRevRangeByScore yerine ZCard+ZRange (1335x hız)",
			msg:   "Motorun [code]ZRevRangeByScore[/code] fonksiyonu limit'i uygulamıyor; tüm skor aralığını malzemeler (~1.8ms / 2.4MB / 20K alloc top-20 için).\n\nÇözüm: [code]ZCard[/code] (O(1)) ile rank penceresini hesapla, tam k üyeyi [code]ZRange[/code] ile oku (O(k)), kendi kendine ters çevir. Ölçülen: [b]1.36µs[/b] — 1335x daha hızlı.\n\nTüm listelemeler bu desende.",
			replies: []string{
				"O(k) sayfalama için tam da doğru yaklaşım. Equal-score tie-breaking nasıl çözülüyor?",
			},
		},
		{
			node: altyapi.ID, user: 3,
			title: "Flarum'dan göç planı (M6)",
			msg:   "M6'da [b]forum.tutorial.com.tr[/b] üzerindeki Flarum 1.8 verisi (MySQL, [code]tuto_[/code] prefix) içeri aktarılacak: kullanıcılar, konular, mesajlar, tepkiler — sonra Flarum kaldırılacak.\n\nSSO için WordPress [i]tutorial-bridge[/i] plugin'iyle hizalanacak (wp_user_id alanı hazır).",
			replies: []string{
				"wp-sync tarafında hangi endpoint'leri kullanacağız? SSO akışını M2'de netleştirelim.",
			},
		},
	}

	for _, t := range threads {
		th, _, err := r.Threads.Create(t.node, t.user, t.title, t.msg)
		if err != nil {
			return fmt.Errorf("seed thread %q: %w", t.title, err)
		}
		for _, rep := range t.replies {
			if _, err := r.Posts.Create(th.ID, t.user, rep); err != nil {
				return fmt.Errorf("seed reply in %q: %w", t.title, err)
			}
		}
	}

	log.Printf("seed: %d category, %d forum, %d thread seeded",
		2, 4, len(threads))
	return nil
}
