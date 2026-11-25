package controlplane

import "sort"

// The largest record of the families of keys that a host, a copy or a send writes (#7248, epic
// #6669). Each is measured on what is stored: records.Encode, or json.Marshal where the object
// is written compact. What is derived from a machine's own copy (its lists, its timings, its
// deploys) is cut to fit, as the machine sends it again; a record that a person's change makes
// is refused (records.Fits).
const (
	// hostCopyBytes is a host's copy: its folders and up to agentsKept agents, of any size.
	hostCopyBytes = 4 << 20
	// copyRecordBytes is a live copy's heartbeat; leaseBytes a host's lease.
	copyRecordBytes = 4 << 10
	leaseBytes      = 1 << 10
	// meshEventBytes is a change event, and meshEventsMost the most kept until the sweep.
	meshEventBytes = 4 << 10
	meshEventsMost = 10_000
	// usageBytes is one period of one machine. usageHoursMost and usageDaysMost are the most
	// periods kept of all machines: the oldest go first.
	usageBytes     = 1 << 20
	usageHoursMost = 200_000
	usageDaysMost  = 100_000
	// buildsDayBytes is one machine's runs of one repository on one day; buildsMetaBytes its meta.
	buildsDayBytes  = 4 << 20
	buildsMetaBytes = 4 << 10
	// deploysBytes is one machine's deploys.
	deploysBytes = 4 << 20
	// projectFoldersBytes is one machine's list of joined folders.
	projectFoldersBytes = 512 << 10
	// runClaimBytes is a workflow run's claim, and runsMost the most kept of all.
	runClaimBytes = 4 << 10
	runsMost      = 100_000
	// The lineage graph: a link, a state, one send's change, and a machine's two files.
	lineageLinkBytes   = 2 << 10
	lineageStateBytes  = 1 << 10
	lineageChangeBytes = 6 << 20
	lineageLinksBytes  = 48 << 20
	lineageStatesBytes = 24 << 20
)

// within refuses data over most bytes, in the words a machine reads.
func within(what string, data []byte, most int) error {
	if len(data) > most {
		return fail(invalidParams, "%s would be too large: %d bytes, and the most is %d.", what, len(data), most)
	}
	return nil
}

// fitting is the most items from the front of items that encode within most bytes, with what
// they encode to. The encoding grows with the items. It fails only when no item fits and the
// empty list is over most too.
func fitting[T any](items []T, most int, encode func([]T) ([]byte, error)) ([]T, []byte, error) {
	n := sort.Search(len(items), func(i int) bool {
		data, err := encode(items[:i+1])
		return err != nil || len(data) > most
	})
	data, err := encode(items[:n])
	if err != nil {
		return nil, nil, err
	}
	if len(data) > most {
		return nil, nil, within("The record", data, most)
	}
	return items[:n], data, nil
}
