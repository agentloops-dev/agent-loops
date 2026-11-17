//go:build !darwin && !linux

package agentsd

// registerSelfUpdate serves nothing here: only a Mac (#2178) and a Linux machine (#5788)
// update themselves.
func (d *Daemon) registerSelfUpdate() {}
