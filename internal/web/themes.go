package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxThemeCSS = 1 << 20

var validThemeID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type themeManifest struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Parent  string `json:"parent,omitempty"`
}

type ThemeOption struct {
	ID, Name, Version string
}

type themeRecord struct {
	manifest themeManifest
	css      []byte
	resolved []byte
	version  string
	state    uint8
}

// ThemeManager resolves trusted, manifest-based theme packages at startup.
// External themes are CSS-only; templates and Go handlers cannot be replaced.
type ThemeManager struct {
	themes map[string]*themeRecord
}

func LoadThemeManager(assets fs.FS, externalDir string) (*ThemeManager, error) {
	m := &ThemeManager{themes: make(map[string]*themeRecord)}
	entries, err := fs.ReadDir(assets, "styles")
	if err != nil {
		return nil, fmt.Errorf("read embedded themes: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		manifestBytes, err := fs.ReadFile(assets, filepath.ToSlash(filepath.Join("styles", id, "theme.json")))
		if err != nil {
			return nil, fmt.Errorf("theme %q manifest: %w", id, err)
		}
		css, err := fs.ReadFile(assets, filepath.ToSlash(filepath.Join("styles", id, "style.css")))
		if err != nil {
			return nil, fmt.Errorf("theme %q stylesheet: %w", id, err)
		}
		if err := m.add(id, manifestBytes, css); err != nil {
			return nil, err
		}
	}
	if externalDir != "" {
		if err := m.loadExternal(externalDir); err != nil {
			return nil, err
		}
	}
	if _, ok := m.themes["thred"]; !ok {
		return nil, fmt.Errorf("required default theme thred is missing")
	}
	for id := range m.themes {
		if _, err := m.resolve(id, nil); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *ThemeManager) add(dirID string, manifestBytes, css []byte) error {
	var manifest themeManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("theme %q manifest is invalid: %w", dirID, err)
	}
	if !validThemeID.MatchString(dirID) || manifest.ID != dirID || strings.TrimSpace(manifest.Name) == "" || strings.TrimSpace(manifest.Version) == "" {
		return fmt.Errorf("theme %q has invalid id, name, or version", dirID)
	}
	if manifest.Parent != "" && !validThemeID.MatchString(manifest.Parent) {
		return fmt.Errorf("theme %q has an invalid parent id", dirID)
	}
	if len(css) == 0 || len(css) > maxThemeCSS {
		return fmt.Errorf("theme %q stylesheet must be between 1 byte and %d bytes", dirID, maxThemeCSS)
	}
	if _, exists := m.themes[dirID]; exists {
		return fmt.Errorf("duplicate theme id %q", dirID)
	}
	m.themes[dirID] = &themeRecord{manifest: manifest, css: css}
	return nil
}

func (m *ThemeManager) loadExternal(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect theme directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("theme directory must be a real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read theme directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		id := entry.Name()
		if !validThemeID.MatchString(id) {
			return fmt.Errorf("invalid external theme directory %q", id)
		}
		dir := filepath.Join(root, id)
		manifestPath, cssPath := filepath.Join(dir, "theme.json"), filepath.Join(dir, "style.css")
		for _, path := range []string{manifestPath, cssPath} {
			if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("theme %q files must be regular, non-symlink files", id)
			}
		}
		manifestBytes, err := os.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("read theme %q manifest: %w", id, err)
		}
		css, err := os.ReadFile(cssPath)
		if err != nil {
			return fmt.Errorf("read theme %q stylesheet: %w", id, err)
		}
		if err := m.add(id, manifestBytes, css); err != nil {
			return err
		}
	}
	return nil
}

func (m *ThemeManager) resolve(id string, stack map[string]bool) ([]byte, error) {
	item, ok := m.themes[id]
	if !ok {
		return nil, fmt.Errorf("theme parent %q is not installed", id)
	}
	if item.state == 2 {
		return item.resolved, nil
	}
	if stack == nil {
		stack = make(map[string]bool)
	}
	if stack[id] || item.state == 1 {
		return nil, fmt.Errorf("theme inheritance cycle at %q", id)
	}
	stack[id], item.state = true, 1
	resolved := make([]byte, 0, len(item.css)+4096)
	if item.manifest.Parent != "" {
		parent, err := m.resolve(item.manifest.Parent, stack)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, parent...)
		resolved = append(resolved, '\n')
	}
	resolved = append(resolved, item.css...)
	delete(stack, id)
	item.resolved = resolved
	sum := sha256.Sum256(resolved)
	item.version = hex.EncodeToString(sum[:12])
	item.state = 2
	return resolved, nil
}

func (m *ThemeManager) Get(id string) (ThemeOption, []byte, bool) {
	item, ok := m.themes[id]
	if !ok || item.state != 2 {
		return ThemeOption{}, nil, false
	}
	return ThemeOption{ID: id, Name: item.manifest.Name, Version: item.version}, item.resolved, true
}

func (m *ThemeManager) Options() []ThemeOption {
	options := make([]ThemeOption, 0, len(m.themes))
	for id, item := range m.themes {
		options = append(options, ThemeOption{ID: id, Name: item.manifest.Name, Version: item.version})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options
}
