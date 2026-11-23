package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Provider is WorkspaceProvider: what the control plane asks of a provider, a Connection
// or a stand-in in tests.
type Provider interface {
	Check() (Checked, error)
	List() ([]MachineInfo, error)
	// Start starts the workspace; its state after, when the provider said.
	Start(id string) (*MachineInfo, error)
	Stop(id string) (*MachineInfo, error)
	// Options is the New Machine form, a JSON Schema object (US5).
	Options() (any, error)
	// Create makes one: what it made when the provider said, and whether firstBoot will run.
	Create(name string, options any, firstBoot *string) (*MachineInfo, bool, error)
	Delete(id string) error
	End()
}

// Saying is a Provider whose make also says what it did, one line a step (#6073), such as
// "Made an ssh key for Agent Loops and added it to your DigitalOcean account.": set-up step
// 1's words. A Connection and a Carried one are; a stand-in need not be.
type Saying interface {
	CreateSaying(name string, options any, firstBoot *string) (made *MachineInfo, runs bool, said []string, err error)
}

// Checked is ProviderConnection.Checked: what Check found.
type Checked struct {
	Capabilities []string
	Contract     int
	Workspaces   []MachineInfo
}

// CallTiming is ProviderConnection.Timing: how long a call may take, a list, then a start
// or stop.
type CallTiming struct {
	Quick time.Duration
	Long  time.Duration
}

// StandardCalls is ProviderConnection.Timing.standard.
var StandardCalls = CallTiming{Quick: 30 * time.Second, Long: 600 * time.Second}

// Contracts is ProviderConnection.contracts: the contract versions this control plane reads.
var Contracts = []int{1}

// Connection is ProviderConnection: one provider's MCP server, reached as its record says,
// the command provider, a server the control plane runs, or one at an address. It checks
// the server is a provider, and calls its tools with the contract's timeouts (FR-005).
type Connection struct {
	Record  ProviderRecord
	server  mcpServer
	mapping ToolMapping
	cwd     string
	timing  CallTiming
	log     func(string)

	connecting sync.Mutex
	mu         sync.Mutex
	client     *mcpClient
}

// NewConnection is ProviderConnection.init: an error when the record cannot be reached as
// it is, or no command provider to run. commandProvider is
// CommandProvider's answer, "" for none.
func NewConnection(record ProviderRecord, secrets map[string]string, cwd string, timing CallTiming,
	commandProvider string, log func(string)) (*Connection, error) {
	server, err := serverFor(record, secrets, commandProvider)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = func(string) {}
	}
	return &Connection{Record: record, server: server, mapping: ContractMapping, cwd: cwd, timing: timing, log: log}, nil
}

// serverFor is ProviderConnection.server(for:): the command provider a record stands for,
// with its secrets as environment.
func serverFor(record ProviderRecord, secrets map[string]string, commandProvider string) (mcpServer, error) {
	if record.Kind != KindCommands {
		return mcpServer{}, providerFailure("It is no kind of provider this control plane knows.")
	}
	if commandProvider == "" {
		return mcpServer{}, providerFailure("workspace-commands isn’t installed beside the control plane.")
	}
	commands := record.Commands
	if commands == nil {
		commands = map[string]any{}
	}
	config, _ := json.Marshal(commands)
	env := map[string]string{}
	for k, v := range secrets {
		env[k] = v
	}
	env["WORKSPACE_COMMANDS_CONFIG"] = string(config)
	return mcpServer{Name: "provider " + record.ID, Command: commandProvider, Args: []string{}, Env: env}, nil
}

// CommandProvider is ProviderConnection.commandProvider: workspace-commands beside the
// running program, then AGENTS_WORKSPACE_COMMANDS, then on PATH; "" for none.
func CommandProvider() string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "workspace-commands"))
	}
	if given := os.Getenv("AGENTS_WORKSPACE_COMMANDS"); given != "" {
		candidates = append(candidates, given)
	}
	for _, folder := range filepath.SplitList(os.Getenv("PATH")) {
		if folder != "" {
			candidates = append(candidates, filepath.Join(folder, "workspace-commands"))
		}
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return c
		}
	}
	return ""
}

// Check is ProviderConnection.check: whether the server is a provider (US2 AS2, AS6), the
// contract's tools, and a contract this control plane reads. It lists once,
// so the list comes back too.
func (c *Connection) Check() (Checked, error) {
	var tools []string
	if err := c.connected(func(client *mcpClient) (err error) {
		tools, err = client.toolNames()
		return err
	}); err != nil {
		return Checked{}, err
	}
	if err := checkTools(tools); err != nil {
		return Checked{}, err
	}
	content, err := c.call(c.mapping.List, map[string]any{"id": ""}, c.timing.Quick)
	if err != nil {
		return Checked{}, err
	}
	contract, err := checkContract(content)
	if err != nil {
		return Checked{}, err
	}
	checked := Checked{Contract: contract, Workspaces: c.mapping.Workspaces(content), Capabilities: []string{}}
	for tool, capability := range optionalTools {
		if contains(tools, tool) {
			checked.Capabilities = append(checked.Capabilities, capability)
		}
	}
	sort.Strings(checked.Capabilities)
	return checked, nil
}

// checkTools says why a server with tools is not a provider: it lacks a contract's tool.
func checkTools(tools []string) error {
	var missing []string
	for _, r := range RequiredTools {
		if !contains(tools, r) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return providerFailure("It isn’t a machine provider: it has no %s tool.", orList(missing))
	}
	return nil
}

// checkContract is the contract a native provider's workspaces_list answer says it
// follows, when this control plane reads it.
func checkContract(content any) (int, error) {
	said, ok := integer(get(content, "contract"))
	if !ok {
		return 0, providerFailure("Its workspaces_list doesn’t say which contract it follows.")
	}
	known := false
	var words []string
	for _, k := range Contracts {
		known = known || k == said
		words = append(words, fmt.Sprint(k))
	}
	if !known {
		return 0, providerFailure("It follows contract %d; this control plane reads %s.", said, strings.Join(words, ", "))
	}
	return said, nil
}

func (c *Connection) List() ([]MachineInfo, error) {
	content, err := c.call(c.mapping.List, map[string]any{"id": ""}, c.timing.Quick)
	if err != nil {
		return nil, err
	}
	return c.mapping.Workspaces(content), nil
}

func (c *Connection) Start(id string) (*MachineInfo, error) { return c.act(c.mapping.Start, id) }
func (c *Connection) Stop(id string) (*MachineInfo, error)  { return c.act(c.mapping.Stop, id) }

func (c *Connection) Options() (any, error) {
	if c.mapping.Options == nil {
		return nil, providerFailure("It has no choices for a new machine.")
	}
	content, err := c.call(*c.mapping.Options, map[string]any{}, c.timing.Quick)
	if err != nil {
		return nil, err
	}
	if schema := get(content, "schema"); schema != nil {
		return schema, nil
	}
	return map[string]any{}, nil
}

func (c *Connection) Create(name string, options any, firstBoot *string) (*MachineInfo, bool, error) {
	made, runs, _, err := c.CreateSaying(name, options, firstBoot)
	return made, runs, err
}

// CreateSaying is Create, with the words the server said for each step (its said, #6073).
func (c *Connection) CreateSaying(name string, options any, firstBoot *string) (*MachineInfo, bool, []string, error) {
	if c.mapping.Create == nil {
		return nil, false, nil, providerFailure("It can’t make workspaces.")
	}
	values := map[string]any{"name": name, "options": options}
	if firstBoot != nil {
		values["firstBoot"] = *firstBoot
	}
	if o, ok := obj(options); ok {
		for k, v := range o {
			if _, taken := values[k]; !taken {
				values[k] = v
			}
		}
	}
	content, err := c.call(*c.mapping.Create, values, c.timing.Long)
	if err != nil {
		return nil, false, nil, err
	}
	runs, _ := boolean(get(content, "firstBootRuns"))
	var said []string
	if lines, ok := get(content, "said").([]any); ok {
		for _, line := range lines {
			if text, ok := line.(string); ok && text != "" {
				said = append(said, text)
			}
		}
	}
	if w := get(content, "workspace"); w != nil {
		return c.mapping.Workspace(w), runs, said, nil
	}
	return nil, runs, said, nil
}

func (c *Connection) Delete(id string) error {
	if c.mapping.Delete == nil {
		return providerFailure("It can’t delete workspaces.")
	}
	_, err := c.call(*c.mapping.Delete, map[string]any{"id": id}, c.timing.Long)
	return err
}

func (c *Connection) act(call Call, id string) (*MachineInfo, error) {
	content, err := c.call(call, map[string]any{"id": id}, c.timing.Long)
	if err != nil {
		return nil, err
	}
	if w := get(content, "workspace"); w != nil {
		return c.mapping.Workspace(w), nil
	}
	return nil, nil
}

// End ends the connection: the server it runs stops.
func (c *Connection) End() {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()
	if client != nil {
		client.end()
	}
}

func (c *Connection) call(call Call, values map[string]any, timeout time.Duration) (any, error) {
	arguments := c.mapping.Arguments(call, values)
	var result any
	if err := c.connected(func(client *mcpClient) (err error) {
		result, err = client.callTool(call.Tool, arguments, timeout)
		return err
	}); err != nil {
		return nil, err
	}
	return Content(result)
}

// connected runs work on a live connection: made when there is none, and made once more
// when the one there has gone (a stdio server that ended, an http session it forgot).
func (c *Connection) connected(work func(*mcpClient) error) error {
	for attempt := range 2 {
		client, err := c.live()
		if err != nil {
			return err
		}
		err = work(client)
		if err == nil {
			return nil
		}
		var failure *mcpFailure
		if !errors.As(err, &failure) {
			return &ProviderFailure{Words: err.Error()}
		}
		// Only a connection that went is tried again: a call the server heard and refused,
		// or ran out of time on, could start a workspace twice.
		c.mu.Lock()
		same := c.client == client
		if attempt == 0 && failure.retriable() && same {
			c.client = nil
		}
		c.mu.Unlock()
		if attempt == 0 && failure.retriable() && same {
			client.end()
			continue
		}
		if failure.kind == "timeout" {
			return providerFailure("It didn’t answer in time.")
		}
		return &ProviderFailure{Words: failure.Error()}
	}
	return providerFailure("It can’t be reached.")
}

// live is the connection's client, connected first when there is none.
func (c *Connection) live() (*mcpClient, error) {
	c.connecting.Lock()
	defer c.connecting.Unlock()
	c.mu.Lock()
	held := c.client
	c.mu.Unlock()
	if held != nil {
		return held, nil
	}
	client := newMCPClient(c.server, c.cwd, c.timing.Quick, c.log)
	if err := client.connect(); err != nil {
		client.end()
		return nil, &ProviderFailure{Words: unreachable(err)}
	}
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
	return client, nil
}

// unreachable is ProviderConnection.unreachable.
func unreachable(err error) string {
	var failure *mcpFailure
	if errors.As(err, &failure) && failure.kind == "auth" {
		return "It wants a sign-in, which providers can’t do yet: give it a token as a secret."
	}
	return "It can’t be reached: " + err.Error()
}

// orList is ProviderConnection.list: "a", "a or b", "a, b or c".
func orList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
