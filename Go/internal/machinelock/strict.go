package machinelock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeStrict reads signed content into v, a pointer. Every reader of the bytes must take the
// same value from them, so it refuses what readers of JSON take in different ways: a name that
// is there twice, one that v's type does not have or that is in another case, text that is not
// UTF-8, half of a surrogate pair, null for the whole, and anything after the value.
func DecodeStrict(content []byte, v any) error {
	if !utf8.Valid(content) {
		return fmt.Errorf("%w: its content is not UTF-8", ErrInvalid)
	}
	if bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
		return fmt.Errorf("%w: its content is null", ErrInvalid)
	}
	if halfPair(content) {
		return fmt.Errorf("%w: its content has half of a character", ErrInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(content))
	if err := strictValue(dec, reflect.TypeOf(v)); err != nil {
		return fmt.Errorf("%w: its content does not read: %v", ErrInvalid, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: its content has more after its end", ErrInvalid)
	}
	if err := json.Unmarshal(content, v); err != nil {
		return fmt.Errorf("%w: its content does not read: %v", ErrInvalid, err)
	}
	return nil
}

// strictValue reads one value, which is to be put in a t.
func strictValue(dec *json.Decoder, t reflect.Type) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		return strictObject(dec, t)
	case json.Delim('['):
		var of reflect.Type
		if t != nil && t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8 {
			of = t.Elem()
		}
		for dec.More() {
			if err := strictValue(dec, of); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	return nil
}

func strictObject(dec *json.Decoder, t reflect.Type) error {
	var fields map[string]reflect.Type
	var of reflect.Type
	if t != nil && t.Kind() == reflect.Struct {
		fields = structFields(t)
	} else if t != nil && t.Kind() == reflect.Map {
		of = t.Elem()
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := tok.(string)
		if seen[name] {
			return fmt.Errorf("%q is there twice", name)
		}
		seen[name] = true
		next := of
		if fields != nil {
			field, ok := fields[name]
			if !ok {
				return fmt.Errorf("%q is not a name it has", name)
			}
			next = field
		}
		if err := strictValue(dec, next); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	return err
}

// halfPair says whether JSON text has half of a surrogate pair: some readers keep the half, and
// others put another character in its place. A backslash is only in a text.
func halfPair(content []byte) bool {
	unit := func(at int) rune {
		if at+6 > len(content) || content[at] != '\\' || content[at+1] != 'u' {
			return 0
		}
		n, err := strconv.ParseUint(string(content[at+2:at+6]), 16, 32)
		if err != nil {
			return 0
		}
		return rune(n) //nolint:gosec // G115: four hexadecimal digits, at most 0xffff
	}
	for i := 0; i < len(content); i++ {
		if content[i] != '\\' {
			continue
		}
		one := unit(i)
		switch {
		case !utf16.IsSurrogate(one):
			i++
		case utf16.DecodeRune(one, unit(i+6)) == utf8.RuneError:
			return true
		default:
			i += 11
		}
	}
	return false
}

func structFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for f := range t.Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			out[name] = f.Type
		}
	}
	return out
}
