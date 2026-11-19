package citimings

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// GitLab pipelines (#5221, spec 5166, "The data spike"): a pipeline from the project's pipeline
// list, its jobs from pipelines/{id}/jobs?include_retried=true, which gives a retried job's
// earlier tries too. GitLab has jobs and no steps, so a job's Steps stay empty.
//
//   - Made is the pipeline's created_at. A job is made when it became pending: its started_at
//     less its queued_duration, so "waiting its turn" is the stages before it, and "waiting for a
//     runner" is queued_duration. A job that GitLab gave no queued_duration is made at its
//     created_at.
//   - Attempt n holds the nth try of each job of one name, oldest first: a retried job is a
//     re-run of it.

// GitLabPipeline is a pipeline as GitLab's list says it.
type GitLabPipeline struct {
	ID        int64  `json:"id"`
	IID       int    `json:"iid"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha"`
	WebURL    string `json:"web_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

var mergeRequestRef = regexp.MustCompile(`^refs/merge-requests/(\d+)/`)

// Finished says a pipeline status is one that is done.
func (p GitLabPipeline) Finished() bool {
	switch p.Status {
	case "success", "failed", "canceled", "skipped":
		return true
	}
	return false
}

type gitLabJob struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	Tag            bool     `json:"tag"`
	CreatedAt      string   `json:"created_at"`
	StartedAt      string   `json:"started_at"`
	FinishedAt     string   `json:"finished_at"`
	QueuedDuration *float64 `json:"queued_duration"`
	Runner         *struct {
		Description string `json:"description"`
	} `json:"runner"`
}

func gitLabConclusion(status string) string {
	switch status {
	case "success":
		return "success"
	case "failed":
		return "failure"
	case "canceled", "canceling":
		return "cancelled"
	case "skipped", "manual":
		return "skipped"
	}
	return status
}

// GitLabJobsFull says a jobs answer is a full page, so there may be more jobs than it gave.
func GitLabJobsFull(raw []byte, perPage int) bool {
	var j []json.RawMessage
	return json.Unmarshal(raw, &j) == nil && len(j) >= perPage
}

// FromGitLab is each attempt of p, from its jobs answer (every page joined into one array): ref
// is its ci-run ref; onDefault and onTag say what its ref is. A job that never ran has no time
// of its own and is made, started and ended when its pipeline was made.
func FromGitLab(p GitLabPipeline, jobsRaw []byte, ref string, onDefault, onTag bool) ([]Run, error) {
	made, err := time.Parse(time.RFC3339, p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("pipeline %d: no made time", p.ID)
	}
	var jobs []gitLabJob
	if err := json.Unmarshal(jobsRaw, &jobs); err != nil {
		return nil, fmt.Errorf("pipeline %d: the jobs answer: %v", p.ID, err)
	}
	// The oldest try of a name first: GitLab numbers its jobs as it makes them.
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].ID < jobs[b].ID })
	since := sinceMade(made, parseRFC3339)
	attempts := newAttempts(gitLabBase(p, jobs, ref, onDefault, onTag, made))
	tries := map[string]int{}
	firstMade := map[int]float64{}
	for _, j := range jobs {
		tries[j.Name]++
		n := tries[j.Name]
		job, ran := gitLabJobOf(j, since)
		if f, ok := firstMade[n]; ran && (!ok || job.Made < f) {
			firstMade[n] = job.Made
		}
		attempts.addJob(n, job)
	}
	last := 1
	for n := range attempts.byN {
		last = max(last, n)
	}
	attempts.of(1)
	for n, r := range attempts.byN {
		finishGitLabAttempt(r, firstMade[n], n == last, p, since)
	}
	return attempts.sorted(), nil
}

// gitLabBase is what each attempt of p has in common; a job of a tag makes it a tag's.
func gitLabBase(p GitLabPipeline, jobs []gitLabJob, ref string, onDefault, onTag bool, made time.Time) Run {
	base := Run{Ref: ref, ID: p.ID, Workflow: p.Name, Event: p.Source, Branch: p.Ref, HeadSHA: p.SHA, Default: onDefault,
		URL: p.WebURL, Made: made.Unix()}
	if base.Workflow == "" {
		base.Workflow = "pipeline"
	}
	if m := mergeRequestRef.FindStringSubmatch(p.Ref); m != nil {
		base.PR, _ = strconv.Atoi(m[1])
	}
	for _, j := range jobs {
		onTag = onTag || j.Tag
	}
	base.Tag = onTag && !onDefault
	return base
}

// gitLabJobOf is a job of the jobs answer; ran says a runner started it.
func gitLabJobOf(j gitLabJob, since sinceFn) (Job, bool) {
	job := Job{Name: j.Name, Conclusion: gitLabConclusion(j.Status)}
	if j.Runner != nil {
		job.Runner = j.Runner.Description
	}
	started, ran := since(j.StartedAt)
	if !ran {
		// It never ran (skipped, manual, or cancelled before a runner took it).
		created, ok := since(j.CreatedAt)
		if !ok {
			created = 0
		}
		job.Made, job.Started, job.Ended = created, created, created
		if e, ok := since(j.FinishedAt); ok {
			job.Ended = e
		}
		return job, false
	}
	job.Started = started
	job.Made, _ = since(j.CreatedAt)
	if j.QueuedDuration != nil {
		job.Made = math.Max(math.Round((started-*j.QueuedDuration)*10)/10, 0)
	}
	var ok bool
	if job.Ended, ok = since(j.FinishedAt); !ok {
		job.Ended = started
	}
	return job, true
}

// finishGitLabAttempt gives an attempt its start, its end and its conclusion: the last the
// pipeline's, an earlier one its jobs'.
func finishGitLabAttempt(r *Run, firstMade float64, last bool, p GitLabPipeline, since sinceFn) {
	s := summarize(r.Jobs, "failure")
	r.Started = firstMade
	if r.Ended = s.ended; r.Ended == 0 {
		r.Ended, _ = since(p.UpdatedAt)
	}
	if last && p.Status != "" {
		r.Conclusion = gitLabConclusion(p.Status)
		return
	}
	// An earlier try was run again, so it did not pass.
	r.Conclusion = earlierConclusion(s.failed, s.cancelled)
}
