package controlplane

// The largest records of the requests, codes and sign-in state (#7249). A new record over its
// largest is refused or cut. One that is kept already always opens, and can be answered.

const (
	// requestsKeptMost is how many requests of one kind are kept, with the ended ones.
	requestsKeptMost = 1000
	// requestCreateBytes is the largest new request, and requestNameMost its longest name. What
	// an answer adds stays within requestMostBytes, so an answer is never refused.
	requestMostBytes   = 8 << 10
	requestCreateBytes = requestMostBytes - 2<<10
	requestNameMost    = 512
)

// fitsRecord refuses a record over its largest size.
func fitsRecord(what string, data []byte, most int) error {
	if len(data) > most {
		return fail(invalidParams, "%s is %d KiB, at most %d KiB.", what, (len(data)+1023)>>10, most>>10)
	}
	return nil
}

// evictEnded drops the oldest ended requests over requestsKeptMost, from requests that are
// oldest first. A request that waits never goes.
func evictEnded[T any](all []T, ended func(T) bool, drop func(T)) []T {
	over := len(all) - requestsKeptMost
	if over <= 0 {
		return all
	}
	out := make([]T, 0, len(all))
	for _, r := range all {
		if over > 0 && ended(r) {
			over--
			drop(r)
			continue
		}
		out = append(out, r)
	}
	return out
}
