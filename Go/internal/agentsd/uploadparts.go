package agentsd

import (
	"os"
	"slices"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The parts of an upload here (#7120). A sender has several parts in flight, so after the part
// at 0, which makes the file, they come in any order. What came is kept here, not in the file:
// a file with a part missing has a hole in it that reads as zeros. An upload with a part
// missing or still being written is refused when it is used.

// mostUploadHoles bounds the holes in one upload: far more than the parts in flight leave.
const mostUploadHoles = 64

type uploadBook struct {
	uploadMu sync.Mutex
	// uploads are the uploads in Incoming, by path.
	uploads map[string]*uploadGot
}

type uploadGot struct {
	// got are the bytes that came, in order, none touching the next.
	got []uploadRange
	// writing counts the parts not yet on the disk.
	writing int
	broken  bool
}

type uploadRange struct{ from, to int64 }

// add notes the bytes from..to; false when some came before, or there are too many holes.
func (u *uploadGot) add(from, to int64) bool {
	i := 0
	for i < len(u.got) && u.got[i].to < from {
		i++
	}
	switch {
	case i < len(u.got) && u.got[i].to == from:
		if i+1 < len(u.got) && u.got[i+1].from < to {
			return false
		}
		u.got[i].to = to
		if i+1 < len(u.got) && u.got[i+1].from == to {
			u.got[i].to = u.got[i+1].to
			u.got = slices.Delete(u.got, i+1, i+2)
		}
	case i < len(u.got) && u.got[i].from < to:
		return false
	case i < len(u.got) && u.got[i].from == to:
		u.got[i].from = from
	case len(u.got) > mostUploadHoles:
		return false
	default:
		u.got = slices.Insert(u.got, i, uploadRange{from, to})
	}
	return true
}

func partMissing() error { return Invalid("A part of the upload is missing. Send again.") }

// writePart writes r, whose bounds receiveUpload checked, into the upload at path.
func (d *Daemon) writePart(r wire.ReceiveUploadRequest, path string) error {
	u, f, err := d.openPart(r, path)
	if err != nil {
		return err
	}
	_, err = f.WriteAt(r.Data, r.Offset)
	if closed := f.Close(); err == nil {
		err = closed
	}
	d.uploadMu.Lock()
	u.writing--
	u.broken = u.broken || err != nil
	d.uploadMu.Unlock()
	if err != nil {
		return fileError(CodeFileNotReadable, err.Error())
	}
	return nil
}

// openPart notes r's bytes in the upload at path, and opens it. The part at 0 starts it again.
func (d *Daemon) openPart(r wire.ReceiveUploadRequest, path string) (*uploadGot, *os.File, error) {
	d.uploadMu.Lock()
	defer d.uploadMu.Unlock()
	flags := os.O_WRONLY
	if r.Offset == 0 {
		sweepTransfers(d.incomingFolder())
		for known := range d.uploads {
			if _, err := os.Stat(known); known == path || err != nil {
				delete(d.uploads, known) // used, or swept
			}
		}
		_ = os.Remove(path) // none there is the same
		if err := os.MkdirAll(d.incomingFolder(), 0o700); err != nil {
			return nil, nil, fileError(CodeFileNotReadable, err.Error())
		}
		if d.uploads == nil {
			d.uploads = map[string]*uploadGot{}
		}
		d.uploads[path] = &uploadGot{}
		flags |= os.O_CREATE
	}
	u := d.uploads[path]
	if u == nil {
		return nil, nil, partMissing()
	}
	// store-bounds: kind=blob; size=8GiB an upload; age=1h; applied=sweepTransfers, at start and at its use
	f, err := os.OpenFile(path, flags, 0o600)
	if os.IsNotExist(err) {
		return nil, nil, partMissing() // swept, or used
	}
	if err != nil {
		return nil, nil, fileError(CodeFileNotReadable, err.Error())
	}
	if len(r.Data) > 0 && !u.add(r.Offset, r.Offset+int64(len(r.Data))) {
		_ = f.Close() // nothing was written
		return nil, nil, partMissing()
	}
	u.writing++
	return u, f, nil
}

// wholeUploads refuses when one of the uploads at paths, "" for none, came with a part missing.
func (d *Daemon) wholeUploads(paths ...string) error {
	d.uploadMu.Lock()
	defer d.uploadMu.Unlock()
	for _, path := range paths {
		u := d.uploads[path]
		if u == nil {
			continue // never came, which its use says
		}
		whole := len(u.got) == 0 || len(u.got) == 1 && u.got[0].from == 0
		if u.broken || u.writing > 0 || !whole {
			return partMissing()
		}
	}
	return nil
}

// dropUpload forgets the upload id and removes what came of it.
func (d *Daemon) dropUpload(id wire.UUID) {
	path, err := d.incomingFile(id)
	if err != nil {
		return
	}
	d.uploadMu.Lock()
	defer d.uploadMu.Unlock()
	delete(d.uploads, path)
	_ = os.Remove(path) // the sweep takes what is left
}
