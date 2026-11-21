package agentsd

import (
	"regexp"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// forgeTitles is each forge's name as a person reads it, by its Name().
var forgeTitles = map[string]string{"github": "GitHub", "gitlab": "GitLab", "linear": "Linear", "jira": "Jira",
	"azureboards": "Azure Boards", "forgejo": "Forgejo", "bitbucket": "Bitbucket", "azuredevops": "Azure DevOps"}

// forgeListError is a forge that could not be read, for a Work page (#6536). A client puts the
// forge's name in front of the message ("GitHub: could not be reached …"), so the message does
// not name the forge again.
func forgeListError(forge string, err error) wire.ForgeListError {
	return wire.ForgeListError{Forge: forge, Message: forgeErrorWords(err.Error())}
}

// transportFailure is Go's url.Error, `Post "https://…": dial tcp …`: the request did not get an answer.
var transportFailure = regexp.MustCompile(`^(?:Get|Head|Post|Put|Patch|Delete) "[^"]*": (.+)$`)

// forgeErrorWords is a forge's error message without the forge's name in front, which each
// forge's client puts there ("GitHub: …"), and with a request that got no answer in words: "could
// not be reached (the connection was refused)", not `Post "…": dial tcp …: connect: connection refused`.
func forgeErrorWords(message string) string {
	for _, title := range forgeTitles {
		if rest, ok := strings.CutPrefix(message, title+": "); ok {
			message = rest
			break
		}
	}
	m := transportFailure.FindStringSubmatch(message)
	if m == nil {
		return message
	}
	cause := strings.ToLower(m[1])
	switch {
	case strings.Contains(cause, "connection refused"):
		return "could not be reached (the connection was refused)"
	case strings.Contains(cause, "no such host"):
		return "could not be reached (no such host)"
	case strings.Contains(cause, "network is unreachable"), strings.Contains(cause, "no route to host"):
		return "could not be reached (no network)"
	case strings.Contains(cause, "timeout"), strings.Contains(cause, "deadline exceeded"):
		return "could not be reached (it did not answer in time)"
	case strings.Contains(cause, "certificate"), strings.Contains(cause, "tls:"):
		return "could not be reached (its certificate is not trusted)"
	case strings.Contains(cause, "context canceled"):
		return "was not read (the request was stopped)"
	}
	return "could not be reached"
}
