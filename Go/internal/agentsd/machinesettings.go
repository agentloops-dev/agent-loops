package agentsd

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// Settings that moved from the data folder to the person's settings (#6745, #6746), and the
// largest record of the small files that hold one record.

// settingStamp tells a changed file from the one read before; the zero stamp is no file.
type settingStamp struct {
	at   time.Time
	size int64
}

func stampOf(path string) settingStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return settingStamp{}
	}
	return settingStamp{at: fi.ModTime(), size: fi.Size()}
}

// moveSetting moves old to file once, and says which of the two to use: old only when it could
// not be moved. A file that is there already wins, as one another machine wrote to a shared
// folder, and old is then kept as <old>.moved.
func moveSetting(old, file string) string {
	if old == "" {
		return file
	}
	b, err := os.ReadFile(old)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("settings: could not read %s: %v", old, err)
		}
		return file
	}
	_, err = os.Stat(file)
	switch {
	case err == nil:
		setMovedAside(old)
		return file
	case !errors.Is(err, fs.ErrNotExist):
		log.Printf("settings: could not move %s: %v", old, err)
		return old
	}
	// store-bounds: not a store: the file itself, in its new place; its own writes declare it
	if err := store.WriteAtomically(file, b, 0o600); err != nil {
		log.Printf("settings: could not move %s: %v", old, err)
		return old
	}
	if err := os.Remove(old); err != nil {
		log.Printf("settings: could not remove %s: %v", old, err)
	}
	log.Printf("settings: moved %s to %s", old, file)
	return file
}

// setMovedAside keeps a file that is not read any more as <old>.moved.
func setMovedAside(old string) {
	if err := os.Rename(old, old+".moved"); err != nil { //nolint:gosec // G703: a fixed name in the daemon's own data folder
		log.Printf("settings: could not set aside %s: %v", old, err) //nolint:gosec // G706: the same path
		return
	}
	log.Printf("settings: kept %s.moved; the one in the settings folder is used", old) //nolint:gosec // G706: the same path
}

// tooLarge refuses a record over the largest its store declares.
func tooLarge(name string, b []byte, most int) error {
	if len(b) <= most {
		return nil
	}
	return fmt.Errorf("%s: %d bytes, at most %d: %w", name, len(b), most, store.ErrLimit)
}
