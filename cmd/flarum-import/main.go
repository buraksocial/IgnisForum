package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/buraksocial/IgnisForum/internal/auth"
	"github.com/buraksocial/IgnisForum/internal/config"
	"github.com/buraksocial/IgnisForum/internal/migration/flarum"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

func main() {
	configPath := flag.String("config", "/etc/ignisforum/config.yaml", "IgnisForum config used for store sizing and production-path safety")
	snapshotPath := flag.String("snapshot", "", "protected Flarum JSON snapshot")
	dataDir := flag.String("data-dir", "", "new isolated staging data directory (required; never the live data directory)")
	reportPath := flag.String("report", "", "new path for the protected JSON reconciliation report (required)")
	flag.Parse()
	if *snapshotPath == "" || *dataDir == "" || *reportPath == "" {
		fatal(errors.New("-snapshot, -data-dir, and -report are required"))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(fmt.Errorf("load IgnisForum config: %w", err))
	}
	if err := ensureStagingPath(cfg.DataDir, *dataDir); err != nil {
		fatal(err)
	}
	if _, err := os.Lstat(*reportPath); err == nil {
		fatal(errors.New("refusing to overwrite an existing reconciliation report"))
	} else if !errors.Is(err, os.ErrNotExist) {
		fatal(fmt.Errorf("inspect reconciliation report path: %w", err))
	}

	info, err := os.Lstat(*snapshotPath)
	if err != nil {
		fatal(fmt.Errorf("inspect snapshot: %w", err))
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		fatal(errors.New("snapshot must be a regular file with no group/other permissions"))
	}
	snapshotFile, err := os.Open(*snapshotPath)
	if err != nil {
		fatal(fmt.Errorf("open snapshot: %w", err))
	}
	snapshot, digest, err := flarum.ReadSnapshot(snapshotFile)
	closeErr := snapshotFile.Close()
	if err != nil {
		fatal(err)
	}
	if closeErr != nil {
		fatal(fmt.Errorf("close snapshot: %w", closeErr))
	}

	stagingAbs, err := filepath.Abs(*dataDir)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(stagingAbs, 0700); err != nil {
		fatal(fmt.Errorf("create staging directory: %w", err))
	}
	if err := os.Chmod(stagingAbs, 0700); err != nil {
		fatal(fmt.Errorf("secure staging directory: %w", err))
	}
	stagingReal, err := filepath.EvalSymlinks(stagingAbs)
	if err != nil {
		fatal(fmt.Errorf("resolve staging directory: %w", err))
	}
	prodAbs, _ := filepath.Abs(cfg.DataDir)
	if prodReal, resolveErr := filepath.EvalSymlinks(prodAbs); resolveErr == nil && pathsOverlap(prodReal, stagingReal) {
		fatal(errors.New("staging directory overlaps the configured production data directory"))
	}

	st, err := store.Open(store.OpenConfig{DataDir: stagingReal, MemtableSize: cfg.MemtableSize, CacheSize: cfg.CacheSize, CompactionThreshold: cfg.CompactionThreshold})
	if err != nil {
		fatal(fmt.Errorf("open staging store: %w", err))
	}
	forumRepos := repo.New(st)
	authService := auth.NewService(st)
	userRepo := auth.NewUserRepo(authService, auth.NewRateLimiter(st))
	report, importErr := flarum.Import(snapshot, digest, st, forumRepos, userRepo)
	closeStoreErr := st.Close()
	if importErr != nil {
		fatal(importErr)
	}
	if closeStoreErr != nil {
		fatal(fmt.Errorf("close staging store: %w", closeStoreErr))
	}

	if err := writePrivateReport(*reportPath, report); err != nil {
		fatal(err)
	}
	exceptions := len(report.Exceptions)
	fmt.Printf("Flarum staging import complete: %d source tables, %d exceptions; report=%s\n", len(report.Tables), exceptions, *reportPath)
}

func ensureStagingPath(productionDir, stagingDir string) error {
	prodAbs, err := filepath.Abs(productionDir)
	if err != nil {
		return err
	}
	stageAbs, err := filepath.Abs(stagingDir)
	if err != nil {
		return err
	}
	if pathsOverlap(filepath.Clean(prodAbs), filepath.Clean(stageAbs)) {
		return errors.New("staging path must be separate from and not contain the production data directory")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
	}
	return within(a, b) || within(b, a)
}

func writePrivateReport(path string, report *flarum.ImportReport) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create reconciliation report: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write reconciliation report: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func fatal(err error) {
	_, _ = io.WriteString(os.Stderr, "flarum-import: "+err.Error()+"\n")
	os.Exit(1)
}
