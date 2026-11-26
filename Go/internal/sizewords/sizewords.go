// Package sizewords writes a size of data as a person reads it: "4.3 MB", "256 KB", "1 GB"
// (#7404, #7407). A refusal never gives raw bytes. Go's standard library has no such
// function, and the libraries that do (go-humanize) have had no release in 12 months, so
// this is ours: one function.
package sizewords

import (
	"fmt"
	"strconv"
)

// Of is n bytes in words, in units of 1024 as the limits are written (4 << 20 is "4 MB").
// It rounds up to one decimal, so a size over a limit never reads as the limit itself:
// 4 MB and 1 byte is "4.1 MB". A whole number has no decimal. Under 1 KB it says bytes.
func Of(n int64) string {
	units := []string{"KB", "MB", "GB", "TB"}
	if n < 1<<10 {
		if n == 1 {
			return "1 byte"
		}
		return strconv.FormatInt(n, 10) + " bytes"
	}
	unit, size := 0, int64(1<<10)
	for unit < len(units)-1 && n >= size<<10 {
		unit, size = unit+1, size<<10
	}
	// Tenths of the unit, rounded up.
	tenths := (n*10 + size - 1) / size
	if tenths >= 10240 && unit < len(units)-1 {
		// 1023.95 KB and more round up to the next unit.
		return Of(size << 10)
	}
	if tenths%10 == 0 {
		return fmt.Sprintf("%d %s", tenths/10, units[unit])
	}
	return fmt.Sprintf("%d.%d %s", tenths/10, tenths%10, units[unit])
}

// Int is Of for an int.
func Int(n int) string { return Of(int64(n)) }
