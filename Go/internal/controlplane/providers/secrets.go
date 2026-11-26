package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
)

// SecretsFileName is ProviderSecrets.fileName: the file that keeps them.
const SecretsFileName = "provider-secrets.json"

const (
	// secretsMost is how many providers' secrets a machine keeps, and secretsRecordBytes the
	// largest of one provider.
	secretsMost        = 100
	secretsRecordBytes = 64 << 10
)

// store-bounds: kind=secret; count=100; size=64KiB a provider (a refreshed token is never refused, and its answer is at most 1 MiB); age=until-deleted; applied=on write, refused, a provider's go when it is forgotten
var secretsFile = store.SecretFile[map[string]map[string]string]{Name: SecretsFileName, Parts: []store.SecretPart[map[string]map[string]string]{
	{What: "providers' secrets", MaxRecordBytes: secretsRecordBytes, MaxCount: secretsMost, Remedy: "remove a provider first",
		Entries: store.Entries[map[string]string]},
}}

// Secrets is ProviderSecrets: each provider's secrets, on the machine that carries it
// (#3398), in ~/.agents-secrets/provider-secrets.json, readable by its user only. The
// control plane keeps none. Never on the wire back; never in the log. Values go only to
// the provider's own server, as its environment or its headers.
type Secrets struct {
	file string
	mu   sync.Mutex
	held map[string]map[string]string
}

// NewSecrets keeps secrets in folder.
func NewSecrets(folder string) *Secrets {
	return &Secrets{file: filepath.Join(folder, SecretsFileName)}
}

// Reload reads the file again at the next use: another process may have written it, as a
// refresh that won a race does (#6072).
func (s *Secrets) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = nil
}

// Values is a provider's secrets, by name.
func (s *Secrets) Values(provider string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.load()[provider] {
		out[k] = v
	}
	return out
}

// Set lays secrets over what the provider had; an empty value takes one away. One over a limit,
// or over a file that does not read, is refused, and the file stays as it is.
func (s *Secrets) Set(provider string, secrets map[string]string) error {
	return s.lay(provider, secrets, secretsFile.Check)
}

// Renew is Set for a refreshed token, which is never refused for its size: the old one is spent.
func (s *Secrets) Renew(provider string, secrets map[string]string) error {
	return s.lay(provider, secrets, func(path string, _ map[string]map[string]string) error {
		_, err := secretsFile.Read(path)
		return err
	})
}

func (s *Secrets) lay(provider string, secrets map[string]string, check func(string, map[string]map[string]string) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := clone(s.load())
	mine := map[string]string{}
	for k, v := range all[provider] {
		mine[k] = v
	}
	for name, value := range secrets {
		if value == "" {
			delete(mine, name)
		} else {
			mine[name] = value
		}
	}
	if len(mine) == 0 {
		delete(all, provider)
	} else {
		all[provider] = mine
	}
	if err := check(s.file, all); err != nil {
		return err
	}
	return s.write(all)
}

// Remove takes a provider's secrets away.
func (s *Secrets) Remove(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := clone(s.load())
	if _, ok := all[provider]; !ok {
		return nil
	}
	delete(all, provider)
	return s.write(all)
}

func clone(all map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(all))
	for k, v := range all {
		out[k] = v
	}
	return out
}

func (s *Secrets) load() map[string]map[string]string {
	if s.held != nil {
		return s.held
	}
	s.held = map[string]map[string]string{}
	if data, err := os.ReadFile(s.file); err == nil {
		var read map[string]map[string]string
		if json.Unmarshal(data, &read) == nil && read != nil {
			s.held = read
		}
	}
	return s.held
}

// write makes the file 0600 before anything is in it, then moves it over the old one.
func (s *Secrets) write(all map[string]map[string]string) error {
	data, err := records.Encode(all)
	if err != nil {
		return err
	}
	folder := filepath.Dir(s.file)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	// store-bounds: not a store: a temporary file, moved over the store
	scratch, err := os.CreateTemp(folder, "."+SecretsFileName+".*")
	if err != nil {
		return &ProviderFailure{Words: "the provider secrets could not be written"}
	}
	name := scratch.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(name)
		}
	}()
	if err := scratch.Chmod(0o600); err != nil {
		scratch.Close()
		return err
	}
	if _, err := scratch.Write(data); err != nil {
		scratch.Close()
		return err
	}
	if err := scratch.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.file); err != nil {
		return err
	}
	ok = true
	// The scratch file was 0600 already: this only keeps an older file's mode right.
	_ = os.Chmod(s.file, 0o600)
	s.held = all
	return nil
}
