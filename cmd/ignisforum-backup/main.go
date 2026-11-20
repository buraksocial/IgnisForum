package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/buraksocial/IgnisForum/internal/backup"
	"github.com/buraksocial/IgnisForum/internal/config"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

func main() {
	configPath := flag.String("config", "/etc/ignisforum/config.yaml", "IgnisForum config")
	action := flag.String("action", "", "backup or restore")
	dataDir := flag.String("data-dir", "", "source directory for backup (defaults to configured data dir)")
	archivePath := flag.String("archive", "", "backup .tar.gz path")
	restoreDir := flag.String("restore-dir", "", "new isolated restore destination")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(fmt.Errorf("load config: %w", err))
	}
	switch *action {
	case "backup":
		source := cfg.DataDir
		if *dataDir != "" {
			source = *dataDir
		}
		if *archivePath == "" {
			fatal(errors.New("-archive is required for backup"))
		}
		if pathsOverlap(source, *archivePath) {
			fatal(errors.New("backup archive must be outside the source data directory"))
		}
		manifest, err := backup.Create(source, *archivePath)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("backup complete: %s (%d files, sha256=%s)\n", *archivePath, manifest.FileCount, manifest.ArchiveSHA256)
	case "restore":
		if *archivePath == "" || *restoreDir == "" {
			fatal(errors.New("-archive and -restore-dir are required for restore"))
		}
		if pathsOverlap(cfg.DataDir, *restoreDir) {
			fatal(errors.New("restore destination must be separate from configured production data directory"))
		}
		manifest, err := backup.Restore(*archivePath, *restoreDir)
		if err != nil {
			fatal(err)
		}
		if err := verifyData(cfg, *restoreDir); err != nil {
			fatal(fmt.Errorf("restored database verification failed (copy retained at %s): %w", *restoreDir, err))
		}
		fmt.Printf("restore verified: %s (%d files, sha256=%s)\n", *restoreDir, manifest.FileCount, manifest.ArchiveSHA256)
	case "verify":
		if *dataDir == "" {
			fatal(errors.New("-data-dir is required for verify"))
		}
		if pathsOverlap(cfg.DataDir, *dataDir) {
			fatal(errors.New("verification target must be separate from configured production data directory"))
		}
		if err := verifyData(cfg, *dataDir); err != nil {
			fatal(err)
		}
		fmt.Printf("database verified: %s\n", *dataDir)
	default:
		fatal(errors.New("-action must be backup, restore, or verify"))
	}
}

func verifyData(cfg config.Config, dataDir string) error {
	oldUmask := syscall.Umask(0077)
	defer syscall.Umask(oldUmask)
	info, err := os.Stat(dataDir)
	if err != nil {
		return fmt.Errorf("inspect verification data directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("verification target is not a directory")
	}
	openCfg := store.OpenConfig{DataDir: dataDir, MemtableSize: cfg.MemtableSize, CacheSize: cfg.CacheSize, CompactionThreshold: cfg.CompactionThreshold}
	st, err := store.Open(openCfg)
	if err != nil {
		return fmt.Errorf("open restored store: %w", err)
	}
	r := repo.New(st)
	report := st.RebuildContent(r.Hooks)
	if err := r.Nodes.RebuildContentCounts(); err != nil {
		_ = st.Close()
		return fmt.Errorf("rebuild node counters: %w", err)
	}
	if err := r.Nodes.BuildTree(); err != nil {
		_ = st.Close()
		return fmt.Errorf("rebuild forum tree: %w", err)
	}
	if err := st.Close(); err != nil {
		return fmt.Errorf("close verified store: %w", err)
	}
	fmt.Fprintf(os.Stderr, "verification rebuild: scanned=%d recovered=%d cleared=%d\n", report.Scanned, report.Recovered, report.Cleared)
	return nil
}

func pathsOverlap(a, b string) bool {
	aAbs, errA := filepath.Abs(a)
	bAbs, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return true
	}
	if real, err := filepath.EvalSymlinks(aAbs); err == nil {
		aAbs = real
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(bAbs)); err == nil {
		bAbs = filepath.Join(parent, filepath.Base(bAbs))
	}
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
		return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
	}
	return within(aAbs, bAbs) || within(bAbs, aAbs)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ignisforum-backup:", err)
	os.Exit(1)
}
