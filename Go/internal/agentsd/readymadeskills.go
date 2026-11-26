package agentsd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// A ready-made skill (#5559), with a workflow or without one (start-a-new-app), is written to
// ~/.agents/skills/<id>/SKILL.md when it is missing, and the digest of what was written is kept
// in madeReadyMadeSkillsPath. A later pin writes its version over a copy the person did not
// edit: one the same as the copy the daemon made, or as an earlier ready-made version. A copy
// the person edited, or a link, stays theirs. A workflow's changed skill makes the workflow
// wait for the person's approval, as a changed workflow does (#5091).

// readyMadeSkillDigest is the sha256 of a skill's text.
func readyMadeSkillDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// madeReadyMadeSkillsPath keeps the readyMadeSkillDigest of each skill copy the daemon made, by id.
func madeReadyMadeSkillsPath(home string) string {
	return filepath.Join(home, ".agents", "settings", "ready-made-skills.json")
}

func madeReadyMadeSkills(home string) map[string]string {
	made := map[string]string{}
	if data, err := os.ReadFile(madeReadyMadeSkillsPath(home)); err == nil {
		_ = json.Unmarshal(data, &made)
	}
	return made
}

func markReadyMadeSkillMade(home, id, digest string) {
	made := madeReadyMadeSkills(home)
	if made[id] == digest {
		return
	}
	if _, had := made[id]; !had && keepsReadyMadeID(madeReadyMadeSkillsPath(home), id, len(made)) != nil {
		return
	}
	made[id] = digest
	data, err := json.Marshal(made)
	if err == nil && os.MkdirAll(filepath.Dir(madeReadyMadeSkillsPath(home)), 0o750) == nil {
		// store-bounds: kind=key-value; count=1_000; size=256B an id; age=until-deleted; applied=on write (one more is refused) (#7349)
		_ = store.WriteAtomically(madeReadyMadeSkillsPath(home), data, 0o600)
	}
}

// ensureReadyMadeSkill writes a ready-made skill when it is missing, and writes today's version
// over a copy the person did not edit. madeDigest is the digest of the copy the daemon made, or "".
func ensureReadyMadeSkill(home, id, skill, madeDigest string) {
	if skill == "" || !ownFolders(home, filepath.Join(".agents", "skills", id)) {
		return
	}
	path := filepath.Join(home, ".agents", "skills", id, "SKILL.md")
	want := readyMadeSkillDigest(skill)
	fi, err := os.Lstat(path)
	update := err == nil
	switch {
	case os.IsNotExist(err):
		if os.MkdirAll(filepath.Dir(path), 0o750) != nil {
			return
		}
	case err != nil || !fi.Mode().IsRegular() || fi.Size() > readyMadeMaxBytes:
		return
	default:
		old, err := os.ReadFile(path)
		if err != nil {
			return
		}
		have := readyMadeSkillDigest(string(old))
		if have == want {
			markReadyMadeSkillMade(home, id, want)
			return
		}
		if have != madeDigest && !earlierReadyMadeSkills[id][have] {
			return // the person's own edit
		}
	}
	// store-bounds: not a store: an install: a ready-made skill into the person's .agents.
	if store.WriteAtomically(path, []byte(skill), 0o644) != nil {
		return
	}
	markReadyMadeSkillMade(home, id, want)
	if update {
		log.Printf("skills: updated the unedited ready-made skill %s to the fetched version", id)
	}
}

// earlierReadyMadeSkills is each ready-made skill's earlier versions, by readyMadeSkillDigest:
// those of this repository before #5091 (Go/internal/agentsd/readymade and readymadeskills) and
// of agentloops-dev/dotagents up to v0.4.0, made before the daemon kept the digest of the copy it
// made, so that such a copy is known to be unedited and is updated too. It is compiled in, never
// read from the repository.
var earlierReadyMadeSkills = map[string]map[string]bool{
	"address-review-comments": {
		"1aded7b6ab50b03e3cb734be4b0fc6244f7b006dd13f67e5769b67dbfc5776b1": true,
		"8581937ce7f483bd2740eb0e842f52d4e75a7e5f0d579da49bcb3d53f2c340c7": true,
		"a79a61f087c4378fe8be177afad2aa4f17803603d37f3b95641d2be2e707bdda": true,
		"deb615fe9d00050b7840b02ff6a6345876aac7f2a313bc0fe910add28b81c5d2": true,
	},
	"answer-a-sign-up-code": {
		"42bc2d69fdd24d0862156568b304d2b5a3c7208ef133aee26f549a954b75ac93": true,
		"cbb983ffd3828b646c2256b9bf2056493772715d723caa6a084b9d1feef1c40f": true,
	},
	"daily-checks": {
		"68f5f3684ef00335c82ab513fdc2b499333167078a6373e03c9fc761d6d4def8": true,
		"a0a53731a57996869201edecb69b2522ef8cabe28d24637257093bf8924d4bec": true,
	},
	"daily-summary": {
		"66dc97294bceb612266ce127ddeb63ed1a7f4e64c2b665d0065c9d607cdaf35d": true,
		"8a21e284e536b872163c4b969772dcd9b6e4dbd1cf96bd05582c194c7f618bf0": true,
		"b6be8e374f01119bbfbc5ff6194da6bc5e4ebac24b4dc7a9d1f476f21cbeeb3b": true,
		"c33b0244f60d6026fc02233316a15652d9982665439c62d64a7f190fa0114e18": true,
	},
	"file-a-bug-from-sentry": {
		"292649a3247f551772c07a970c885f080341479663cf2ff7e8e984dad7cd6213": true,
	},
	"fix-failed-checks": {
		"0c12820fc58779434ed2cdd091b65cfca397f287efa8264b19f31f1125dd791b": true,
		"4d333f76f804d457a17384a17706c3854ee6e13fc21a382e34a53f771baaf11b": true,
		"806b20a9bde6106d0b534d380192ecc82621451947bfaf8ed88b736550bcacfa": true,
		"9fc97c1cb6fe5dc0b448491cd0ea7ea2f1ffb3ea04c0ac417e0f0ece81efb6c4": true,
		"da9062c4aaecc5fed530b095e5613bce8565d581966adcff7312ba72fe65dffd": true,
	},
	"map-a-docs-server": {
		"3b4a937f4d67b5eab0c2b395849cef43769834c59de3af13efe547a1d4ec1188": true,
		"507ee7983505a4d3958c66a61a31179d06610aa38daf5cffd810011910c93bad": true,
	},
	"start-a-new-app": {
		"3cf042214ba4b599dc7d1e4c58c09f06c9d6202722aaabe8ca2fa27e8eb35557": true,
		"3ea634d6cd9880d7059d629cd545d2b36a886160a7d014cbc07fb1794b3456d0": true,
		"4f6688efd367b483ff62d5daa82ced85614c5c2c3257b00c7692d5f8d81e98aa": true,
		"6aa4401cc0a1bec844fe87f12ce9cc98a4ea3bb5ed39b9f7ff52446e17a78da5": true,
		"728511892a9b9d0c4fb25c20e45cdced715518ffc0f4d677fbd792b8c467f9e3": true,
		"9699d58910a66cd58f9b83627352431fb3dec84f1449bb7bb360433c9d6dbc08": true,
		"b77518ecfec4e767746dad53e573048d6d401bb0cf621b4d9187964778456935": true,
		"f3ec19ddc033bb03ca9048f0cfb9ab0842f96f35a4d3eb3bb101febc21c18359": true,
	},
	"work-a-jira-issue": {
		"0e001e45a6e80d983799ba45d2817ea4f7b19956b93a2509b80154a699527808": true,
		"687a5a7f04e10987b659ab6ca1dc34cc8064829f257644657683114af7bee84f": true,
	},
	"work-a-linear-issue": {
		"05129ec5f1c30ae7a6e00f8d401ddf441b082bedb893de2e58eb31153f99512d": true,
		"64655469f6f1f13a7772995edce167c94514c42fa328a89896cb3cdb991ba98c": true,
		"7830595c61bc3e23df3c43914a7cd8259f93970ef8d3f6c157609a27fe0edbf5": true,
		"d646feee1bfa3c1a0a13085c3883a10e128b57e33b6c6860679e15d293ebec24": true,
		"e0a05ebbe9ad7b8226f0dbd1260289621a2f52d51d6ddcd29612f9d6c36eef42": true,
	},
	"work-an-issue": {
		"000dfe9bf717019231a932b1e0bc514f9968dc4b8571df18b2b3eda9c82cbc46": true,
		"1ffc3b336c1d9a17d4690fcab908581366e6739630ff6ac1f9f3b5d647afc253": true,
		"38e59f60b29677977bec6054b9892932f1aacc1234e14b1be9af4a5a8075207f": true,
		"41fc51295a49a31ae2c0b6e7f8932371aa3b130d54e6debaeadeea36b3eac514": true,
		"8d573a9b8e3e0982e29d0e2120a49feda567022572289910bb2977ca3c05483c": true,
		"997973f21b088f604d8f649a6dc424f1f8907e6b2b4fbe1d235e0289a1284c40": true,
		"c62ac0ccbe6326073b86cccf8f7f1d3e575fd946257df17cab91bc45ae45e636": true,
		"ee2a41644d580b185452874de3136004d10570536786e92cd9bfe2aa48ffc525": true,
	},
}
