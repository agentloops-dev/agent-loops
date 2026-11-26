// Package execfile writes a file that this process runs at once, such as a script (#6369).
package execfile

import (
	"os"
	"syscall"
)

// Write writes data to path with perm, as os.WriteFile does, and holds syscall.ForkLock
// for reading while the file is open for writing. On Linux, a child that another goroutine
// forks while the file is open inherits the write descriptor until it execs, and until then
// the kernel refuses to exec the file: "text file busy" (ETXTBSY, golang/go#22315). The
// lock keeps every fork of this process out of that window, so an exec of path after Write
// returns does not fail with it. A rename into place does not help: the inherited
// descriptor still holds the same inode open for writing.
func Write(path string, data []byte, perm os.FileMode) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	// store-bounds: not a store: a helper; each caller declares the file that it writes
	return os.WriteFile(path, data, perm)
}
