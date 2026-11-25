package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A log's file that another machine or an older build wrote (a restore, an import, a move)
// is held to the record limit before it is a store's (#7183). A line over the limit is never
// held in memory whole, and its place among the lines is kept.

// LineHead is what is known of a line over the limit: the top-level values that come whole in
// its first bytes, and its time.
type LineHead struct {
	Values map[string]json.RawMessage
	At     wire.Date
}

// BoundTranscript holds the transcript in file to the record limit of an append: an entry over
// it is a notice with the same id and time. It says whether any was.
func BoundTranscript(file string) (bool, error) {
	return boundLines(file, transcriptRecord, func(h LineHead, size int64) ([]byte, error) {
		var id wire.UUID
		if json.Unmarshal(h.Values["id"], &id) != nil || id == "" {
			id = NewUUID()
		}
		return diskjson.Marshal(tooLarge(id, h.At, size))
	})
}

// BoundAppTools holds the record of app tool calls in file to its record limit: cut is what is
// kept in place of a call over it.
func BoundAppTools(file string, cut func(h LineHead, size int64) ([]byte, error)) error {
	_, err := boundLines(file, appToolsRecord, cut)
	return err
}

// boundLines puts cut's line in place of each line of file over limit. A file with none is
// left as it is.
func boundLines(file string, limit int, cut func(h LineHead, size int64) ([]byte, error)) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	over, err := copyBounded(io.Discard, f, limit, nil)
	if err != nil || !over {
		return false, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	tmp := file + ".bounded"
	// store-bounds: not a store: a staged file, which takes the first one's place
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) //nolint:gosec // G302: the mode a transcript always had
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	w := bufio.NewWriter(out)
	_, err = copyBounded(w, f, limit, cut)
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	return true, os.Rename(tmp, file)
}

// copyBounded copies lines, and cut's line for each one over limit, and says whether there was
// one. With no cut it stops at the first.
func copyBounded(to io.Writer, from io.Reader, limit int, cut func(h LineHead, size int64) ([]byte, error)) (bool, error) {
	r := bufio.NewReaderSize(from, 1<<16)
	var head, before []byte // the first bytes of this line, and the line before it
	over := false
	for {
		head = head[:0]
		var size int64 // without its newline
		var err error
		ended := false
		for !ended {
			var chunk []byte
			chunk, err = r.ReadSlice('\n')
			ended = !errors.Is(err, bufio.ErrBufferFull)
			chunk = bytes.TrimSuffix(chunk, []byte{'\n'})
			size += int64(len(chunk))
			head = append(head, chunk[:min(len(chunk), limit-len(head))]...)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return over, err
		}
		line := head
		if size > int64(limit) {
			over = true
			if cut == nil {
				return true, nil
			}
			var cerr error
			if line, cerr = cut(lineHead(head, before), size); cerr != nil {
				return over, cerr
			}
		}
		if _, werr := to.Write(line); werr != nil {
			return over, werr
		}
		if err != nil { // the end: what follows the last newline is not a line
			if size > int64(limit) {
				_, err = to.Write([]byte{'\n'})
				return over, err
			}
			return over, nil
		}
		if _, werr := to.Write([]byte{'\n'}); werr != nil {
			return over, werr
		}
		if cut != nil {
			before = append(before[:0], line...)
		}
	}
}

// lineHead reads the first bytes of a line. A line that does not say its time has the time of
// the line before it, so a trim by age goes on in order.
func lineHead(head, before []byte) LineHead {
	h := LineHead{Values: map[string]json.RawMessage{}}
	dec := json.NewDecoder(bytes.NewReader(head))
	if t, err := dec.Token(); err == nil && t == json.Delim('{') {
		for {
			t, err := dec.Token()
			key, ok := t.(string)
			if err != nil || !ok {
				break
			}
			var v json.RawMessage
			if dec.Decode(&v) != nil {
				break
			}
			h.Values[key] = v
		}
	}
	var at string
	if json.Unmarshal(h.Values["at"], &at) == nil {
		if d, err := diskjson.ParseDate(at); err == nil {
			h.At = d
			return h
		}
	}
	if _, t, ok := RecordStamp(before); ok {
		h.At = wire.DateOf(t)
		return h
	}
	h.At = wire.DateOf(time.Now())
	return h
}
