package agentsd

import "path/filepath"

// setToolsHome is where a Mac host's own toolsets are: the root's cache's tools/, where the
// Swift host installs them (048), so a host that changes language finds them.
func (d *Daemon) setToolsHome() {
	toolsHomesMu.Lock()
	tools, server := toolsHome, serverToolsets
	toolsHomesMu.Unlock()
	setToolsHomes(d.macToolsFolder(), tools, server)
}

// macToolsFolder is where this Mac daemon's own toolsets are: <cache>/tools (048), or the
// one folder that every daemon of a test process shares (sharedToolsHome).
func (d *Daemon) macToolsFolder() string {
	if sharedToolsHome != "" {
		return sharedToolsHome
	}
	return filepath.Join(d.Store.Loc.Cache, "tools")
}
