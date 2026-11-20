// Package plugin runs capability-free WebAssembly extensions. Plugins receive
// JSON values and return JSON values; they get no host functions, WASI, network,
// filesystem, clocks, or direct access to IgnisForum storage.
package plugin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	APIVersion       = 1
	maxManifestBytes = 64 << 10
	maxModuleBytes   = 8 << 20
	maxPayloadBytes  = 64 << 10
	maxMemoryPages   = 256 // 16 MiB
	callTimeout      = 100 * time.Millisecond
	maxPluginCount   = 16
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

type Dependency struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Manifest is the stable plugin package descriptor. Version dependencies are
// exact matches to keep startup resolution deterministic and auditable.
type Manifest struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Version    string       `json:"version"`
	APIVersion int          `json:"api_version"`
	Hooks      []string     `json:"hooks"`
	Depends    []Dependency `json:"depends,omitempty"`
}

type Status struct {
	ID, Name, Version string
	Hooks             []string
	Disabled          bool   // explicitly disabled by the administrator
	BlockedBy         string // inactive dependency, if any
}

type loadedPlugin struct {
	manifest Manifest
	module   wazero.CompiledModule
}

// Manager owns a WASM runtime. A nil/empty plugin directory is a valid
// installation and produces an empty manager.
type Manager struct {
	runtime wazero.Runtime
	plugins []loadedPlugin
}

func Load(ctx context.Context, dir string) (*Manager, error) {
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(maxMemoryPages).
		WithCloseOnContextDone(true))
	m := &Manager{runtime: runtime}
	if dir == "" {
		return m, nil
	}
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("inspect plugin directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		_ = runtime.Close(ctx)
		return nil, errors.New("plugin directory must be a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("read plugin directory: %w", err)
	}
	manifests := make(map[string]Manifest, len(entries))
	paths := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		id := entry.Name()
		if !idPattern.MatchString(id) {
			return nil, m.fail(ctx, fmt.Errorf("invalid plugin directory name %q", id))
		}
		pkgDir := filepath.Join(dir, id)
		manifestPath := filepath.Join(pkgDir, "plugin.json")
		modulePath := filepath.Join(pkgDir, "plugin.wasm")
		manifestBytes, err := readRegular(manifestPath, maxManifestBytes)
		if err != nil {
			return nil, m.fail(ctx, fmt.Errorf("plugin %q manifest: %w", id, err))
		}
		var manifest Manifest
		dec := json.NewDecoder(strings.NewReader(string(manifestBytes)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&manifest); err != nil {
			return nil, m.fail(ctx, fmt.Errorf("plugin %q manifest: %w", id, err))
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return nil, m.fail(ctx, fmt.Errorf("plugin %q manifest has trailing JSON", id))
		}
		if err := validateManifest(id, manifest); err != nil {
			return nil, m.fail(ctx, err)
		}
		if _, exists := manifests[id]; exists {
			return nil, m.fail(ctx, fmt.Errorf("duplicate plugin id %q", id))
		}
		manifests[id], paths[id] = manifest, modulePath
		if len(manifests) > maxPluginCount {
			return nil, m.fail(ctx, fmt.Errorf("at most %d plugins may be installed", maxPluginCount))
		}
	}
	order, err := resolveDependencies(manifests)
	if err != nil {
		return nil, m.fail(ctx, err)
	}
	for _, id := range order {
		wasm, err := readRegular(paths[id], maxModuleBytes)
		if err != nil {
			return nil, m.fail(ctx, fmt.Errorf("plugin %q module: %w", id, err))
		}
		compiled, err := runtime.CompileModule(ctx, wasm)
		if err != nil {
			return nil, m.fail(ctx, fmt.Errorf("plugin %q WebAssembly validation: %w", id, err))
		}
		if err := validateExports(compiled); err != nil {
			_ = compiled.Close(ctx)
			return nil, m.fail(ctx, fmt.Errorf("plugin %q ABI: %w", id, err))
		}
		// No host modules are instantiated in this runtime, so any import
		// (including WASI or a plugin-defined capability) cannot be resolved.
		probe, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName("validate-"+id).WithStartFunctions())
		if err != nil {
			_ = compiled.Close(ctx)
			return nil, m.fail(ctx, fmt.Errorf("plugin %q imports or initialization are not allowed: %w", id, err))
		}
		_ = probe.Close(ctx)
		m.plugins = append(m.plugins, loadedPlugin{manifest: manifests[id], module: compiled})
	}
	return m, nil
}

func (m *Manager) fail(ctx context.Context, err error) error {
	_ = m.Close(ctx)
	return err
}

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("must be a regular non-symlink file")
	}
	if info.Size() == 0 || info.Size() > limit {
		return nil, fmt.Errorf("file size must be between 1 and %d bytes", limit)
	}
	return os.ReadFile(path)
}

func validateManifest(dirID string, manifest Manifest) error {
	if manifest.ID != dirID || !idPattern.MatchString(manifest.ID) || strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 100 || strings.TrimSpace(manifest.Version) == "" || len(manifest.Version) > 64 {
		return fmt.Errorf("plugin %q has invalid id, name, or version", dirID)
	}
	if manifest.APIVersion != APIVersion {
		return fmt.Errorf("plugin %q uses API %d; supported API is %d", dirID, manifest.APIVersion, APIVersion)
	}
	if len(manifest.Hooks) == 0 || len(manifest.Hooks) > 16 {
		return fmt.Errorf("plugin %q must declare 1 to 16 hooks", dirID)
	}
	seenHooks := make(map[string]bool)
	for _, hook := range manifest.Hooks {
		if hook != "home.decorate" || seenHooks[hook] {
			return fmt.Errorf("plugin %q declares unsupported or duplicate hook %q", dirID, hook)
		}
		seenHooks[hook] = true
	}
	seenDeps := make(map[string]bool)
	for _, dep := range manifest.Depends {
		if !idPattern.MatchString(dep.ID) || dep.Version == "" || len(dep.Version) > 64 || seenDeps[dep.ID] || dep.ID == dirID {
			return fmt.Errorf("plugin %q has invalid dependency", dirID)
		}
		seenDeps[dep.ID] = true
	}
	return nil
}

func resolveDependencies(manifests map[string]Manifest) ([]string, error) {
	ids := make([]string, 0, len(manifests))
	for id := range manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state := make(map[string]uint8, len(ids))
	order := make([]string, 0, len(ids))
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("plugin dependency cycle at %q", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		manifest := manifests[id]
		for _, dep := range manifest.Depends {
			installed, ok := manifests[dep.ID]
			if !ok {
				return fmt.Errorf("plugin %q requires missing plugin %q", id, dep.ID)
			}
			if installed.Version != dep.Version {
				return fmt.Errorf("plugin %q requires %s@%s, installed version is %s", id, dep.ID, dep.Version, installed.Version)
			}
			if err := visit(dep.ID); err != nil {
				return err
			}
		}
		state[id] = 2
		order = append(order, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func validateExports(module wazero.CompiledModule) error {
	if len(module.ExportedMemories()) != 1 {
		return errors.New("exactly one exported linear memory is required")
	}
	if _, ok := module.ExportedMemories()["memory"]; !ok {
		return errors.New("exported memory must be named memory")
	}
	functions := module.ExportedFunctions()
	for name, signature := range map[string][2][]api.ValueType{
		"ignis_plugin_api": {nil, {api.ValueTypeI32}},
		"ignis_alloc":      {{api.ValueTypeI32}, {api.ValueTypeI32}},
		"ignis_hook":       {{api.ValueTypeI32, api.ValueTypeI32}, {api.ValueTypeI64}},
	} {
		function, ok := functions[name]
		if !ok || !sameTypes(function.ParamTypes(), signature[0]) || !sameTypes(function.ResultTypes(), signature[1]) {
			return fmt.Errorf("export %s has a missing or incompatible signature", name)
		}
	}
	return nil
}

func sameTypes(a, b []api.ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RunHook invokes installed plugins in deterministic dependency order. The ABI
// is ignis_plugin_api()->i32 (must return 1), ignis_alloc(i32)->i32, and
// ignis_hook(i32 ptr,i32 len)->i64 (high 32 bits=output ptr, low 32=length).
// Invalid plugin results are returned as errors and are never trusted as HTML.
func (m *Manager) RunHook(ctx context.Context, hook string, input any) ([][]byte, error) {
	return m.RunEnabledHook(ctx, hook, input, nil)
}

// RunEnabledHook applies one immutable administrative settings snapshot.
func (m *Manager) RunEnabledHook(ctx context.Context, hook string, input any, disabled map[string]bool) ([][]byte, error) {
	if m == nil || len(m.plugins) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode plugin input: %w", err)
	}
	if len(encoded) > maxPayloadBytes {
		return nil, errors.New("plugin input exceeds payload limit")
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var results [][]byte
	var errs []error
	blocked := m.inactive(disabled)
	for _, item := range m.plugins {
		if disabled[item.manifest.ID] || blocked[item.manifest.ID] != "" || !contains(item.manifest.Hooks, hook) {
			continue
		}
		result, err := m.invoke(ctx, item, hook, encoded)
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: %w", item.manifest.ID, err))
			continue
		}
		results = append(results, result)
	}
	return results, errors.Join(errs...)
}

func (m *Manager) invoke(parent context.Context, item loadedPlugin, hook string, input []byte) ([]byte, error) {
	ctx := parent
	mod, err := m.runtime.InstantiateModule(ctx, item.module, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("instantiate: %w", err)
	}
	defer mod.Close(context.Background())
	apiFn := mod.ExportedFunction("ignis_plugin_api")
	apiResult, err := apiFn.Call(ctx)
	if err != nil || len(apiResult) != 1 || apiResult[0] != 1 {
		return nil, errors.New("unsupported plugin ABI version")
	}
	mem := mod.ExportedMemory("memory")
	hookBytes, _ := json.Marshal(struct {
		Hook string          `json:"hook"`
		Data json.RawMessage `json:"data"`
	}{Hook: hook, Data: input})
	// The ABI receives a single JSON envelope so new hook data can be added
	// compatibly without changing function signatures.
	allocResult, err := mod.ExportedFunction("ignis_alloc").Call(ctx, uint64(len(hookBytes)))
	if err != nil || len(allocResult) != 1 {
		if err == nil {
			return nil, errors.New("allocate hook payload returned an invalid result")
		}
		return nil, fmt.Errorf("allocate hook payload: %w", err)
	}
	if allocResult[0] > uint64(^uint32(0)) {
		return nil, errors.New("plugin returned an invalid payload pointer")
	}
	payloadPtr := uint32(allocResult[0])
	if !mem.Write(payloadPtr, hookBytes) {
		return nil, errors.New("plugin payload buffer is out of bounds")
	}
	result, err := mod.ExportedFunction("ignis_hook").Call(ctx, uint64(payloadPtr), uint64(len(hookBytes)))
	if err != nil || len(result) != 1 {
		if err == nil {
			return nil, errors.New("hook returned an invalid result")
		}
		return nil, fmt.Errorf("run hook: %w", err)
	}
	packed := result[0]
	outPtr, outLen := uint32(packed>>32), uint32(packed)
	if outLen > maxPayloadBytes {
		return nil, errors.New("plugin output exceeds payload limit")
	}
	output, ok := mem.Read(outPtr, outLen)
	if !ok {
		return nil, errors.New("plugin output buffer is out of bounds")
	}
	output = append([]byte(nil), output...)
	if !json.Valid(output) {
		return nil, errors.New("plugin output is not valid JSON")
	}
	return output, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (m *Manager) Status() []Status {
	return m.ConfiguredStatus(nil)
}

func (m *Manager) ConfiguredStatus(disabled map[string]bool) []Status {
	if m == nil {
		return nil
	}
	out := make([]Status, 0, len(m.plugins))
	blocked := m.inactive(disabled)
	for _, item := range m.plugins {
		id := item.manifest.ID
		out = append(out, Status{ID: id, Name: item.manifest.Name, Version: item.manifest.Version, Hooks: append([]string(nil), item.manifest.Hooks...), Disabled: disabled[id], BlockedBy: blocked[id]})
	}
	return out
}

// Plugins are already sorted with dependencies first by Load.
func (m *Manager) inactive(disabled map[string]bool) map[string]string {
	if len(disabled) == 0 {
		return nil
	}
	blocked := make(map[string]string)
	for _, item := range m.plugins {
		for _, dep := range item.manifest.Depends {
			if disabled[dep.ID] || blocked[dep.ID] != "" {
				blocked[item.manifest.ID] = dep.ID
				break
			}
		}
	}
	return blocked
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil || m.runtime == nil {
		return nil
	}
	return m.runtime.Close(ctx)
}

// EncodeResult packs a memory pointer and length as required by the guest ABI.
// It is exported for plugin authors implementing the IgnisForum guest runtime.
func EncodeResult(ptr, length uint32) uint64 {
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], length)
	binary.LittleEndian.PutUint32(buf[4:8], ptr)
	return binary.LittleEndian.Uint64(buf[:])
}
