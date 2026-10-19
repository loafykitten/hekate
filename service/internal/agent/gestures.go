package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// gestureSource lists the gesture clips in the asset manifest. The manifest is
// re-read when it changes on disk, so replacing the assets needs no restart.
type gestureSource struct {
	dir string

	mu      sync.Mutex
	modTime time.Time
	size    int64
	names   []string
}

func newGestureSource(assetsDir string) *gestureSource { return &gestureSource{dir: assetsDir} }

// Names returns the gesture names, or nil when there is no manifest.
func (g *gestureSource) Names() ([]string, error) {
	if g == nil || g.dir == "" {
		return nil, nil
	}
	path := filepath.Join(g.dir, "manifest.json")
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat manifest: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.names != nil && st.ModTime().Equal(g.modTime) && st.Size() == g.size {
		return g.names, nil
	}
	names, err := readGestures(path)
	if err != nil {
		return nil, err
	}
	g.modTime, g.size, g.names = st.ModTime(), st.Size(), names
	return names, nil
}

// readGestures returns the names of animations in the "gesture" category.
func readGestures(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m struct {
		Animations []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"animations"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	names := []string{}
	for _, a := range m.Animations {
		if a.Category == "gesture" && a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return names, nil
}
