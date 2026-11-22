package controlplane

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Each machine's answer in one call (#5592, epic #5281): Work's lists and boards, Reports'
// forecast and features, Events' newest page and the connections are asked of every machine of
// the caller's by the control plane, in one call, and each machine's answer comes back with it,
// as it gave it. Unlike docs/listAll these are not merged here: the machines list the same repos,
// boards and allowances, and the client already merges them by ref. Each call is bounded as
// fanOut's: at most mostFanMachines machines, each for at most fanPatience.

// fanEachAnswer is each machine's answer, in the order of hosts, and those that did not answer.
type fanEachAnswer[T any] struct {
	Items     []fanItem[T]
	Unreached []string
}

// fanEach asks each host at once, and keeps the answers in the order of hosts.
func fanEach[T any](hosts []string, ask func(host string) (T, bool)) fanEachAnswer[T] {
	got := make([]T, len(hosts))
	answered := make([]bool, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Go(func() { got[i], answered[i] = ask(host) })
	}
	wg.Wait()
	out := fanEachAnswer[T]{Items: []fanItem[T]{}}
	for i, host := range hosts {
		if answered[i] {
			out.Items = append(out.Items, fanItem[T]{Host: host, Item: got[i]})
		} else {
			out.Unreached = append(out.Unreached, host)
		}
	}
	return out
}

// askEach asks each of the caller's machines with the capability the same method, and gives
// each answer to into, with the machine's id and name.
func askEach[T any](m *Methods, user, capability, method string, params any, from Caller, into func(host, name string, answer T)) (unreached []wire.ProjectMachine, notAsked int64) {
	hosts, skipped := m.fanHosts(user, capability)
	got := fanEach(hosts, func(host string) (T, bool) {
		var answer T
		return answer, m.askHost(host, method, params, from, &answer)
	})
	for _, item := range got.Items {
		into(item.Host, m.hostName(item.Host), item.Item)
	}
	return m.fanProjectMachines(got.Unreached), int64(skipped)
}

const (
	// A host's forge/issues and forge/pulls take 1 to 100, and events/list's page is the window's.
	mostForgePage   = 100
	defaultEvents   = 50
	mostEventsPage  = 100
	forgeKindIssues = "issues"
	forgeKindPulls  = "pulls"
)

// fanAllMethod answers the methods of fanall.go.
func (m *Methods) fanAllMethod(_ context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	user := m.s.actorFor(from.Client).User
	switch method {
	case wire.CallForgeListAll.Name:
		request, err := require[wire.ForgeListAllRequest](params, "ForgeListAllRequest")
		if err != nil {
			return nil, err
		}
		return m.forgeListAll(request, user, from)
	case wire.CallForgeBoardsAll.Name:
		out := wire.ForgeBoardsAll{Boards: []wire.MachineForgeBoards{}}
		out.Unreached, out.NotAsked = askEach(m, user, wire.CapForgeBoards1, wire.CallForgeBoards.Name, wire.ForgeBoardsRequest{}, from,
			func(host, name string, b wire.ForgeBoards) {
				out.Boards = append(out.Boards, wire.MachineForgeBoards{Machine: wire.MachineRef(host), MachineName: name, Boards: b})
			})
		return out, nil
	case wire.CallLineageForecastAll.Name:
		request, err := require[wire.LineageForecastRequest](params, "LineageForecastRequest")
		if err != nil {
			return nil, err
		}
		out := wire.LineageForecastAll{Forecasts: []wire.MachineLineageForecast{}}
		out.Unreached, out.NotAsked = askEach(m, user, wire.CapLineageForecast1, wire.CallLineageForecast.Name, request, from,
			func(host, name string, f wire.LineageForecast) {
				out.Forecasts = append(out.Forecasts, wire.MachineLineageForecast{Machine: wire.MachineRef(host), MachineName: name, Forecast: f})
			})
		return out, nil
	case wire.CallReportsFeaturesAll.Name:
		out := wire.FeatureUsageAll{Usage: []wire.MachineFeatureUsage{}}
		out.Unreached, out.NotAsked = askEach(m, user, wire.CapFeatures1, wire.CallReportsFeatures.Name, wire.Empty{}, from,
			func(host, name string, u wire.FeatureUsage) {
				out.Usage = append(out.Usage, wire.MachineFeatureUsage{Machine: wire.MachineRef(host), MachineName: name, Usage: u})
			})
		return out, nil
	case wire.CallEventsListAll.Name:
		request, err := require[wire.EventsListAllRequest](params, "EventsListAllRequest")
		if err != nil {
			return nil, err
		}
		ask := wire.EventsListRequest{Limit: int64(fanLimit(request.Limit, defaultEvents, mostEventsPage))}
		out := wire.EventsListAll{Pages: []wire.MachineEventsPage{}}
		out.Unreached, out.NotAsked = askEach(m, user, wire.CapEvents1, wire.CallEventsList.Name, ask, from,
			func(host, name string, page wire.EventsPage) {
				out.Pages = append(out.Pages, wire.MachineEventsPage{Machine: wire.MachineRef(host), MachineName: name, Page: page})
			})
		return out, nil
	case wire.CallConnectionsListAll.Name:
		out := wire.ConnectionsListAll{Lists: []wire.MachineConnections{}}
		out.Unreached, out.NotAsked = askEach(m, user, wire.CapConnections1, wire.CallConnectionsList.Name, wire.Empty{}, from,
			func(host, name string, c wire.ConnectionsList) {
				out.Lists = append(out.Lists, wire.MachineConnections{Machine: wire.MachineRef(host), MachineName: name, Connections: c})
			})
		return out, nil
	}
	return nil, notFound(method)
}

// forgeListAll is forge/listAll: each machine's forge/issues or forge/pulls. With pages, only the
// machines it names (of the caller's, with forge/1) are asked, each from its cursor.
func (m *Methods) forgeListAll(request wire.ForgeListAllRequest, user string, from Caller) (wire.ForgeListAll, error) {
	method := wire.CallForgeIssues.Name
	switch request.Kind {
	case forgeKindIssues:
	case forgeKindPulls:
		method = wire.CallForgePulls.Name
	default:
		return wire.ForgeListAll{}, fail(invalidParams, "kind is issues or pulls.")
	}
	capability := wire.CapForge1
	if deref(request.Board) != "" {
		// Only a machine that knows boards is asked for a board's list, as one that does not would list it all (#3282).
		capability = wire.CapForgeBoards1
	}
	hosts, notAsked := m.fanHosts(user, capability)
	cursors := map[string]string{}
	if request.Pages != nil {
		if len(request.Pages) > mostFanMachines {
			return wire.ForgeListAll{}, fail(invalidParams, "That is more pages than one call asks.")
		}
		for _, p := range request.Pages {
			cursors[string(p.Machine)] = p.Cursor
		}
		// Only the machines named, and of those only the caller's: a machine that is not theirs is left out.
		named := []string{}
		for _, host := range hosts {
			if _, ok := cursors[host]; ok {
				named = append(named, host)
			}
		}
		hosts, notAsked = named, 0
	}
	limit := new(int64(fanLimit(request.Limit, 50, mostForgePage)))
	got := fanEach(hosts, func(host string) (wire.ForgeList, bool) {
		// A label, an assignee, the sort and a Refresh's fresh (#6642) go to each machine, so a narrowed list is one call (#6383, #6538).
		ask := wire.ForgeListRequest{State: request.State, Board: request.Board, Label: request.Label, Assignee: request.Assignee, Sort: request.Sort,
			Limit: limit, Fresh: request.Fresh}
		if c, ok := cursors[host]; ok && c != "" {
			ask.Cursor = &c
		}
		var list wire.ForgeList
		return list, m.askHost(host, method, ask, from, &list)
	})
	out := wire.ForgeListAll{Lists: []wire.MachineForgeList{}, NotAsked: int64(notAsked)}
	for _, item := range got.Items {
		out.Lists = append(out.Lists, wire.MachineForgeList{Machine: wire.MachineRef(item.Host), MachineName: m.hostName(item.Host), List: item.Item})
	}
	out.Unreached = m.fanProjectMachines(got.Unreached)
	return out, nil
}
