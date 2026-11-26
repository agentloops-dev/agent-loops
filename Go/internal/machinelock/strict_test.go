package machinelock

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSignedContentReadsInOneWay(t *testing.T) {
	type inner struct {
		Text string `json:"text"`
	}
	type content struct {
		Text  string            `json:"text"`
		Inner *inner            `json:"inner,omitzero"`
		Names map[string]string `json:"names,omitzero"`
		List  []inner           `json:"list,omitzero"`
		Any   json.RawMessage   `json:"any,omitzero"`
	}
	for _, good := range []string{
		`{"text":"a"}`, ` { "text" : "é😀\ufffd�\ud83d\ude00\\ud800" } `, `{}`, `{"inner":null,"names":{"a":"1","A":"2"}}`,
		`{"list":[{"text":"a"},{"text":"b"}],"any":{"x":[{"y":1}]}}`,
	} {
		var c content
		if err := DecodeStrict([]byte(good), &c); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, bad := range []string{
		``, `null`, ` null `, `[]`, `"a"`, `{`, `{"text":"a"}x`, `{"text":"a"}{}`, `{"text":"a"`, `{"text":1}`,
		`{"text":"a","text":"b"}`, `{"text":"a","Text":"b"}`, `{"Text":"a"}`, `{"text":"a","text":"b"}`, `{"other":1}`,
		`{"inner":{"text":"a","Text":"b"}}`, `{"list":[{"text":"a","other":1}]}`, `{"names":{"a":"1","a":"2"}}`,
		`{"any":{"x":[{"y":1,"y":2}]}}`, `{"text":"\ud800"}`, `{"text":"\ud800\u0041"}`, `{"text":"\\\ud800"}`, `{"text":"\ude00\ud83d"}`, `{"names":{"\udc00":"1"}}`, "{\"text\":\"\xff\"}",
	} {
		var c content
		if err := DecodeStrict([]byte(bad), &c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestASetUpScriptsApprovalIsItsHashAndItsSize(t *testing.T) {
	hash := ContentHash([]byte("x"))
	if a, err := SetupScriptOf([]byte(`{"hash":"` + hash + `","size":1}`)); err != nil || a.Hash != hash || a.Size != 1 {
		t.Fatalf("%+v, %v", a, err)
	}
	for _, bad := range []string{``, `{}`, `{"hash":"` + hash + `"}`, `{"hash":"` + hash + `","size":0}`, `{"hash":"` + hash + `","size":1,"x":1}`,
		`{"hash":"` + hash[1:] + `","size":1}`, `{"hash":"` + "A" + hash[1:] + `","size":1}`, `{"hash":"` + hash + `","size":1,"Size":2}`,
		`{"hash":"` + hash + `","size":1.5}`} {
		if _, err := SetupScriptOf([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
