package metadata

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

type toolResult struct {
	path string
	err  error
}

// toolCache caches FindTool lookups for the life of the process. The
// executable location and PATH are effectively constant, so this is the one
// piece of package state; it is guarded by toolMu.
var (
	toolMu    sync.Mutex
	toolCache = map[string]toolResult{}
)

// FindTool looks for an external tool next to the running executable first
// (with .exe on Windows; elsewhere it must have an execute bit), then on
// PATH. Results are cached.
func FindTool(name string) (string, error) {
	toolMu.Lock()
	defer toolMu.Unlock()
	if r, ok := toolCache[name]; ok {
		return r.path, r.err
	}
	p, err := findTool(name)
	toolCache[name] = toolResult{p, err}
	return p, err
}

func findTool(name string) (string, error) {
	file := name
	if runtime.GOOS == "windows" && filepath.Ext(file) == "" {
		file += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		cand := filepath.Join(filepath.Dir(exe), file)
		if st, err := os.Stat(cand); err == nil && st.Mode().IsRegular() &&
			(runtime.GOOS == "windows" || st.Mode()&0o111 != 0) {
			return cand, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		// exec.ErrDot (tool only found in the current directory) is an
		// error too: never run a tool from there implicitly.
		return "", fmt.Errorf("find %s: %w", name, err)
	}
	return p, nil
}
