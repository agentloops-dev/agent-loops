package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

type cliCommand struct {
	name  string
	args  string
	about string
}

var cliCommands = []cliCommand{
	{"/help", "", "show loops' commands and the runtime's"},
	{"/exit", "", "leave this chat; the session goes on"},
	{"/stop", "", "stop the turn"},
	{"/new", "", "leave the session and start another with the next prompt"},
	{"/resume", "", "choose a recent session here"},
	{"/rename", "<title>", "give the session a title"},
	{"/status", "", "show the session's settings, folder and machine"},
	{"/mode", "[name]", "show or set the permission mode"},
	{"/model", "[name]", "show or set the model"},
	{"/effort", "[level]", "show or set how hard the agent works"},
}

type cliChoice struct {
	value string
	name  string
}

var cliModeLevels = map[string]wire.PermissionLevel{
	"plan":               wire.PermissionLevelPlan,
	"ask":                wire.PermissionLevelAsk,
	"edits":              wire.PermissionLevelApproveForMe,
	"approve-for-me":     wire.PermissionLevelApproveForMe,
	"auto":               wire.PermissionLevelBypassPermissions,
	"bypass-permissions": wire.PermissionLevelBypassPermissions,
}

var cliModelLevels = map[string]wire.ModelLevel{
	"fast":     wire.ModelLevelFast,
	"balanced": wire.ModelLevelBalanced,
	"smart":    wire.ModelLevelSmart,
}

func parseSlash(text string) (name, arg string, ok bool) {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "/") {
		return "", "", false
	}
	name, arg, _ = strings.Cut(s, " ")
	name = strings.ToLower(strings.TrimPrefix(name, "/"))
	if name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(arg), true
}

func knownCliCommand(name string) bool {
	return slices.ContainsFunc(cliCommands, func(c cliCommand) bool { return c.name == "/"+name })
}

func (f *live) command(ctx context.Context, text string) (bool, error) {
	name, arg, ok := parseSlash(text)
	if !ok || !knownCliCommand(name) {
		return false, nil
	}
	switch name {
	case "help":
		return true, f.commandHelp(ctx)
	case "exit":
		return true, errLeave
	case "stop":
		return true, f.commandStop(ctx)
	case "new":
		return true, f.commandNew()
	case "resume":
		return true, f.commandResume(ctx)
	case "rename":
		return true, f.commandRename(ctx, arg)
	case "status":
		return true, f.commandStatus(ctx)
	case "mode":
		return true, f.commandMode(ctx, arg)
	case "model":
		return true, f.commandModel(ctx, arg)
	case "effort":
		return true, f.commandEffort(ctx, arg)
	}
	return false, nil
}

func (f *live) needSession() error {
	if f.id == "" {
		return errors.New("start a session first, or type what it should do")
	}
	return nil
}

func (f *live) sessionRecord(ctx context.Context) (wire.Agent, error) {
	if err := f.needSession(); err != nil {
		return wire.Agent{}, err
	}
	r, err := f.c.Call(ctx, f.host(), "agents/list", wire.ListRequest{AgentID: &f.id, IncludeArchived: true})
	if err != nil {
		return wire.Agent{}, unreachable(f.p.machineName(), err)
	}
	var list []wire.Agent
	if err := json.Unmarshal(r, &list); err != nil {
		return wire.Agent{}, err
	}
	if len(list) == 0 {
		return wire.Agent{}, fmt.Errorf("the session is no longer on %s", f.p.machineName())
	}
	return list[0], nil
}

func (f *live) loadOffers(ctx context.Context) ([]wire.ConfigOption, error) {
	if f.offers != nil {
		return f.offers, nil
	}
	a, err := f.sessionRecord(ctx)
	if err != nil {
		return nil, err
	}
	if len(f.commands) == 0 && len(a.AvailableCommands) > 0 {
		f.commands = a.AvailableCommands
	}
	if a.RuntimeID == "" {
		if len(a.AdvertisedOptions) == 0 {
			return nil, fmt.Errorf("%s did not say what it offers", runtimeOf(a))
		}
		f.offers = a.AdvertisedOptions
		f.updateSettings(a.RuntimeID, a.AdvertisedOptions)
		return f.offers, nil
	}
	r, err := f.c.Call(ctx, f.host(), "agents/options", wire.OptionsRequest{RuntimeID: a.RuntimeID, Cwd: f.p.folder, McpServers: []wire.MCPServer{}})
	if err != nil {
		return nil, unreachable(f.p.machineName(), err)
	}
	var got wire.OptionsResponse
	if err := json.Unmarshal(r, &got); err != nil {
		return nil, err
	}
	if got.DraftID != "" {
		_, _ = f.c.Call(ctx, f.host(), "agents/discardDraft", wire.DiscardDraftRequest{DraftID: got.DraftID})
	}
	if len(f.commands) == 0 && len(got.Commands) > 0 {
		f.commands = got.Commands
	}
	if len(got.Options) == 0 {
		got.Options = a.AdvertisedOptions
	}
	f.offers = got.Options
	f.updateSettings(a.RuntimeID, got.Options)
	return f.offers, nil
}

func (f *live) updateSettings(runtime string, offers []wire.ConfigOption) {
	if f.settings == nil {
		f.settings = map[string]string{}
	}
	if runtime != "" {
		f.settings["runtime"] = runtime
	}
	for _, kind := range []string{"model", "mode", "effort"} {
		o := optionFor(offers, kind)
		if o == nil {
			continue
		}
		v := jsonValueText(o.CurrentValue)
		if v == "" || f.settings[kind] != "" {
			continue
		}
		choices := choicesOfOption(o)
		if c, ok := resolveChoice(choices, v); ok {
			v = c.name
		}
		f.settings[kind] = v
	}
}

func (f *live) settingsLine() string {
	if f.id == "" {
		return ""
	}
	var parts []string
	for _, k := range []string{"runtime", "model", "mode", "effort"} {
		if v := f.settings[k]; v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "  " + f.t.st.paint(roleQuiet, strings.Join(parts, " · "))
}

func (f *live) printCommand(lines ...string) {
	f.print(lines...)
}

func (f *live) commandFail(what string, err error) {
	f.printCommand(f.t.st.paint(roleFailure, "Could not "+what+": "+err.Error()))
}

func (f *live) commandHelp(ctx context.Context) error {
	f.printCommand(f.t.st.bold("loops commands"))
	for _, c := range cliCommands {
		name := c.name
		if c.args != "" {
			name += " " + c.args
		}
		f.printCommand(fmt.Sprintf("  %-16s %s", name, c.about))
	}
	if err := f.needSession(); err != nil {
		f.printCommand(f.t.st.paint(roleQuiet, "Start a session to see its runtime's commands."))
		return nil
	}
	if _, err := f.loadOffers(ctx); err != nil {
		f.printCommand(f.t.st.paint(roleFailure, "The runtime's commands are not here: "+err.Error()))
		return nil
	}
	f.printCommand("", f.t.st.bold("This runtime's commands"))
	if len(f.commands) == 0 {
		f.printCommand(f.t.st.paint(roleQuiet, "  none offered"))
		return nil
	}
	for _, c := range f.commands {
		name := "/" + c.Name
		if c.Description != nil && *c.Description != "" {
			name += "  " + *c.Description
		}
		f.printCommand("  " + oneLine(name))
	}
	return nil
}

func (f *live) commandStop(ctx context.Context) error {
	if err := f.needSession(); err != nil {
		return err
	}
	if !f.sent {
		f.printCommand(f.t.st.paint(roleQuiet, "No turn is running."))
		return nil
	}
	if _, err := f.c.Call(ctx, f.host(), "agents/stop", wire.AgentStopRequest{AgentID: f.id}); err != nil {
		return err
	}
	f.printCommand(f.t.st.paint(roleQuiet, "Stopping the turn."))
	f.sent, f.working = false, false
	f.state = wire.AgentStateStopped
	return nil
}

func (f *live) commandNew() error {
	if f.id != "" {
		f.printCommand(f.t.st.paint(roleQuiet, "Left "+f.short()+". That session goes on."))
	}
	f.printCommand(f.t.st.paint(roleQuiet, "Started a new session; type what it should do."))
	f.id, f.state, f.title = "", "", ""
	f.cards = nil
	f.offers, f.commands, f.settings = nil, nil, nil
	f.since, f.stopAt = time.Time{}, time.Time{}
	f.sent, f.working = false, false
	f.next = 0
	f.tr = newTranscript(f.t.st)
	return nil
}

func (f *live) commandResume(ctx context.Context) error {
	if err := f.needSession(); err != nil {
		return err
	}
	if f.t.keys == nil {
		list, err := recentSessions(ctx, f.c, f.p)
		if err != nil {
			return err
		}
		printRecent(f.t.out, f.t.st, f.p, list, time.Now())
		f.printCommand(f.t.st.paint(roleQuiet, "Use loops -r <id> to resume one."))
		return nil
	}
	a, _, err := f.pickRecent(ctx)
	if err != nil {
		return err
	}
	if a == nil {
		f.printCommand(f.t.st.paint(roleQuiet, "Kept the current session."))
		return nil
	}
	if err := f.attach(ctx, *a, 0); err != nil {
		return err
	}
	return nil
}

func (f *live) commandRename(ctx context.Context, arg string) error {
	if err := f.needSession(); err != nil {
		return err
	}
	title := oneLine(arg)
	if title == "" {
		return fmt.Errorf("give a title: /rename <title>")
	}
	if _, err := f.c.Call(ctx, f.host(), "agents/rename", wire.RenameRequest{AgentID: f.id, Title: title}); err != nil {
		return err
	}
	f.title = title
	f.printCommand(f.t.st.paint(roleDone, "Renamed to "+fmt.Sprintf("%q", title)+"."))
	return nil
}

func (f *live) commandStatus(ctx context.Context) error {
	a, err := f.sessionRecord(ctx)
	if err != nil {
		return err
	}
	if len(f.commands) == 0 && len(a.AvailableCommands) > 0 {
		f.commands = a.AvailableCommands
	}
	if len(f.offers) == 0 && len(a.AdvertisedOptions) > 0 {
		f.offers = a.AdvertisedOptions
		f.updateSettings(a.RuntimeID, f.offers)
	}
	settings := map[string]string{"runtime": runtimeOf(a)}
	for k, v := range f.settings {
		if v != "" {
			settings[k] = v
		}
	}
	f.printCommand(f.t.st.bold("Session "+f.short()),
		fmt.Sprintf("  title:   %s", titleOf(a)),
		fmt.Sprintf("  runtime: %s", settings["runtime"]),
		fmt.Sprintf("  model:   %s", valueOr(settings["model"], "not set")),
		fmt.Sprintf("  mode:    %s", valueOr(settings["mode"], "not set")),
		fmt.Sprintf("  effort:  %s", valueOr(settings["effort"], "not set")),
		fmt.Sprintf("  folder:  %s", f.p.folderName()),
		fmt.Sprintf("  machine: %s", f.p.machineName()))
	return nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (f *live) commandMode(ctx context.Context, arg string) error {
	offers, err := f.loadOffers(ctx)
	if err != nil {
		return err
	}
	o := optionFor(offers, "mode")
	choices := choicesOfOption(o)
	if arg == "" {
		f.printCommand(fmt.Sprintf("  Mode: %s", f.currentLabel("mode", o)))
		f.printChoices("modes", choices)
		return nil
	}
	if p, ok := cliModeLevels[strings.ToLower(arg)]; ok {
		level := p
		if _, err := f.c.Call(ctx, f.host(), "agents/setLevels", wire.SetLevelsRequest{AgentID: f.id, Levels: wire.Levels{Permission: &level}}); err != nil {
			return err
		}
		f.setSetting("mode", permissionName(level))
		f.printCommand(f.t.st.paint(roleDone, "Mode set to "+permissionName(level)+". It applies from the next step."))
		return nil
	}
	c, ok := resolveChoice(choices, arg)
	if !ok {
		return f.unsupported("mode", arg, choices)
	}
	if o == nil {
		return fmt.Errorf("this runtime has no mode to set")
	}
	if _, err := f.c.Call(ctx, f.host(), "agents/setOption", wire.SetOptionRequest{AgentID: f.id, OptionID: o.ID, Value: jsonString(c.value)}); err != nil {
		return err
	}
	f.setSetting("mode", c.name)
	f.printCommand(f.t.st.paint(roleDone, "Mode set to "+c.name+". It applies from the next step."))
	return nil
}

func (f *live) commandModel(ctx context.Context, arg string) error {
	offers, err := f.loadOffers(ctx)
	if err != nil {
		return err
	}
	o := optionFor(offers, "model")
	choices := choicesOfOption(o)
	if arg == "" {
		f.printCommand(fmt.Sprintf("  Model: %s", f.currentLabel("model", o)))
		f.printChoices("models", choices)
		return nil
	}
	if m, ok := cliModelLevels[strings.ToLower(arg)]; ok {
		level := m
		if _, err := f.c.Call(ctx, f.host(), "agents/setLevels", wire.SetLevelsRequest{AgentID: f.id, Levels: wire.Levels{Model: &level}}); err != nil {
			return err
		}
		f.setSetting("model", modelName(level))
		f.printCommand(f.t.st.paint(roleDone, "Model set to "+modelName(level)+". It applies from the next step."))
		return nil
	}
	c, ok := resolveChoice(choices, arg)
	if !ok {
		return f.unsupported("model", arg, choices)
	}
	if o == nil {
		return fmt.Errorf("this runtime has no model to set")
	}
	if _, err := f.c.Call(ctx, f.host(), "agents/setOption", wire.SetOptionRequest{AgentID: f.id, OptionID: o.ID, Value: jsonString(c.value)}); err != nil {
		return err
	}
	f.setSetting("model", c.name)
	f.printCommand(f.t.st.paint(roleDone, "Model set to "+c.name+". It applies from the next step."))
	return nil
}

func (f *live) commandEffort(ctx context.Context, arg string) error {
	offers, err := f.loadOffers(ctx)
	if err != nil {
		return err
	}
	o := optionFor(offers, "effort")
	choices := choicesOfOption(o)
	if arg == "" {
		f.printCommand(fmt.Sprintf("  Effort: %s", f.currentLabel("effort", o)))
		f.printChoices("effort levels", choices)
		return nil
	}
	c, ok := resolveChoice(choices, arg)
	if !ok {
		return f.unsupported("effort", arg, choices)
	}
	if o == nil {
		return fmt.Errorf("this runtime has no effort to set")
	}
	if _, err := f.c.Call(ctx, f.host(), "agents/setOption", wire.SetOptionRequest{AgentID: f.id, OptionID: o.ID, Value: jsonString(c.value)}); err != nil {
		return err
	}
	f.setSetting("effort", c.name)
	f.printCommand(f.t.st.paint(roleDone, "Effort set to "+c.name+". It applies from the next step."))
	return nil
}

func (f *live) printChoices(what string, choices []cliChoice) {
	if len(choices) == 0 {
		f.printCommand(f.t.st.paint(roleQuiet, "  No choices offered."))
		return
	}
	f.printCommand(f.t.st.paint(roleQuiet, "  Offered "+what+":"))
	for i, c := range choices {
		f.printCommand(fmt.Sprintf("    %d. %s", i+1, c.name))
	}
}

func (f *live) unsupported(kind, arg string, choices []cliChoice) error {
	var values []string
	for _, c := range choices {
		values = append(values, c.value)
	}
	f.printCommand(f.t.st.paint(roleFailure, fmt.Sprintf("%q is not a %s. Offered: %s.", arg, kind, strings.Join(values, ", "))))
	return nil
}

func (f *live) currentLabel(kind string, o *wire.ConfigOption) string {
	if v := f.settings[kind]; v != "" {
		return v
	}
	if o == nil {
		return "not set"
	}
	v := jsonValueText(o.CurrentValue)
	if v == "" {
		return "not set"
	}
	if c, ok := resolveChoice(choicesOfOption(o), v); ok {
		return c.name
	}
	return v
}

func (f *live) setSetting(kind, value string) {
	if f.settings == nil {
		f.settings = map[string]string{}
	}
	f.settings[kind] = value
}

func optionFor(offers []wire.ConfigOption, kind string) *wire.ConfigOption {
	for _, o := range offers {
		switch kind {
		case "model":
			if o.ID == "model" || (o.Category != nil && *o.Category == "model") || strings.Contains(strings.ToLower(o.Name), "model") {
				return &o
			}
		case "mode":
			if o.ID == "mode" || (o.Type == "select" && o.Category != nil && *o.Category == "mode") {
				return &o
			}
		case "effort":
			if (o.Category != nil && *o.Category == "thought_level") || strings.Contains(o.ID, "effort") || strings.Contains(strings.ToLower(o.Name), "effort") {
				return &o
			}
		}
	}
	return nil
}

func choicesOfOption(o *wire.ConfigOption) []cliChoice {
	if o == nil {
		return nil
	}
	var items []struct {
		Value   string `json:"value"`
		Name    string `json:"name"`
		Options []struct {
			Value string `json:"value"`
			Name  string `json:"name"`
		} `json:"options"`
	}
	if json.Unmarshal(o.Options, &items) != nil {
		return nil
	}
	var out []cliChoice
	for _, it := range items {
		if it.Value != "" {
			name := it.Name
			if name == "" {
				name = it.Value
			}
			out = append(out, cliChoice{it.Value, name})
		}
		for _, g := range it.Options {
			name := g.Name
			if name == "" {
				name = g.Value
			}
			out = append(out, cliChoice{g.Value, name})
		}
	}
	return out
}

func resolveChoice(choices []cliChoice, arg string) (cliChoice, bool) {
	arg = strings.TrimSpace(arg)
	if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(choices) {
		return choices[n-1], true
	}
	for _, c := range choices {
		if strings.EqualFold(c.value, arg) || strings.EqualFold(c.name, arg) {
			return c, true
		}
	}
	return cliChoice{}, false
}

func jsonValueText(v wire.JSONValue) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return strings.Trim(strings.TrimSpace(string(v)), `"`)
}

func permissionName(p wire.PermissionLevel) string {
	switch p {
	case wire.PermissionLevelPlan:
		return "plan"
	case wire.PermissionLevelAsk:
		return "ask"
	case wire.PermissionLevelApproveForMe:
		return "approve-for-me"
	case wire.PermissionLevelBypassPermissions:
		return "bypass-permissions"
	}
	return string(p)
}

func modelName(m wire.ModelLevel) string { return string(m) }

func jsonString(s string) wire.JSONValue {
	b, _ := json.Marshal(s)
	return b
}
