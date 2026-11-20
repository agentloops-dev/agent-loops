package citimings

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"time"
)

// GitHub Actions (spec 5166, "Is the forge's data enough? Yes, on GitHub"): a run from the run
// list (actions/runs), its jobs and steps from actions/runs/{id}/jobs?filter=all, which gives
// every attempt's jobs, each with its own run_attempt. One line is kept for each attempt.

// GitHubRun is a run as GitHub's run list says it.
type GitHubRun struct {
	ID           int64  `json:"id"`
	RunAttempt   int    `json:"run_attempt"`
	CreatedAt    string `json:"created_at"`
	RunStartedAt string `json:"run_started_at"`
	UpdatedAt    string `json:"updated_at"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	Event        string `json:"event"`
	Path         string `json:"path"`
	HTMLURL      string `json:"html_url"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

// GitHubRunList is a page of actions/runs.
type GitHubRunList struct {
	TotalCount   int         `json:"total_count"`
	WorkflowRuns []GitHubRun `json:"workflow_runs"`
}

type gitHubJobs struct {
	TotalCount int         `json:"total_count"`
	Jobs       []gitHubJob `json:"jobs"`
}

type gitHubJob struct {
	RunAttempt  int      `json:"run_attempt"`
	Name        string   `json:"name"`
	CreatedAt   string   `json:"created_at"`
	StartedAt   string   `json:"started_at"`
	CompletedAt string   `json:"completed_at"`
	Conclusion  string   `json:"conclusion"`
	Labels      []string `json:"labels"`
	Steps       []struct {
		Name        string `json:"name"`
		Number      int    `json:"number"`
		StartedAt   string `json:"started_at"`
		CompletedAt string `json:"completed_at"`
		Conclusion  string `json:"conclusion"`
	} `json:"steps"`
}

// FromGitHub is each attempt of run, from its jobs answer: ref is its ci-run ref; onDefault and
// onTag say what its branch is. An attempt with no job that ran is still kept: it counts.
func FromGitHub(run GitHubRun, jobsRaw []byte, ref string, onDefault, onTag bool) ([]Run, error) {
	made, err := time.Parse(time.RFC3339, run.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("run %d: no made time", run.ID)
	}
	var jobs gitHubJobs
	if err := json.Unmarshal(jobsRaw, &jobs); err != nil {
		return nil, fmt.Errorf("run %d: the jobs answer: %v", run.ID, err)
	}
	since := sinceMade(made, parseRFC3339)
	attempts := newAttempts(gitHubBase(run, ref, onDefault, onTag, made))
	for _, j := range jobs.Jobs {
		attempts.addJob(max(j.RunAttempt, 1), gitHubJobOf(j, since))
	}
	// The run's own attempt is in the list even with no jobs; its conclusion is the run's.
	latest := attempts.of(max(run.RunAttempt, 1))
	latest.Conclusion = run.Conclusion
	if s, ok := since(run.RunStartedAt); ok {
		latest.Started = s
	}
	for n, r := range attempts.byN {
		finishGitHubAttempt(r, n, run, since)
	}
	return attempts.sorted(), nil
}

// gitHubBase is what each attempt of run has in common.
func gitHubBase(run GitHubRun, ref string, onDefault, onTag bool, made time.Time) Run {
	base := Run{Ref: ref, ID: run.ID, Workflow: path.Base(run.Path), Event: run.Event, Branch: run.HeadBranch,
		HeadSHA: run.HeadSHA, Default: onDefault, Tag: onTag && !onDefault, URL: run.HTMLURL, Made: made.Unix()}
	if len(run.PullRequests) > 0 {
		base.PR = run.PullRequests[0].Number
	}
	return base
}

// gitHubJobOf is a job of the jobs answer, with its steps that say both times.
func gitHubJobOf(j gitHubJob, since sinceFn) Job {
	job := Job{Name: j.Name, Conclusion: j.Conclusion}
	job.Made, _ = since(j.CreatedAt)
	var ok bool
	if job.Started, ok = since(j.StartedAt); !ok {
		job.Started = job.Made
	}
	if job.Ended, ok = since(j.CompletedAt); !ok {
		job.Ended = job.Started
	}
	if len(j.Labels) > 0 {
		job.Runner = j.Labels[0]
	}
	for _, s := range j.Steps {
		a, okA := since(s.StartedAt)
		b, okB := since(s.CompletedAt)
		if okA && okB {
			job.Steps = append(job.Steps, Step{Name: s.Name, Number: s.Number, Started: a, Ended: b, Conclusion: s.Conclusion})
		}
	}
	return job
}

// finishGitHubAttempt gives attempt n of run its end, an earlier attempt its start, and an
// earlier attempt its conclusion, from its jobs.
func finishGitHubAttempt(r *Run, n int, run GitHubRun, since sinceFn) {
	s := summarize(r.Jobs, "failure", "timed_out")
	if r.Ended = s.ended; r.Ended == 0 {
		r.Ended, _ = since(run.UpdatedAt)
	}
	if r.Started == 0 && !math.IsInf(s.made, 1) && n != run.RunAttempt {
		r.Started = s.made
	}
	if r.Conclusion == "" {
		// An earlier attempt's conclusion is its jobs': it was run again, so it did not pass.
		r.Conclusion = earlierConclusion(s.failed, s.cancelled)
	}
}
