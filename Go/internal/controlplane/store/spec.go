package store

// Validate checks a spec's kind and bounds, for a backend of a kind outside this package
// (the host's folder logs).
func (s Spec) Validate(kind string) error { return validateSpec(s, kind) }
