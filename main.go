package main

import (
	"os"
	"time"
)

// StateMachine walks through the state machine using a given GithubClient
// interface until a terminal state is reached.
// It begins by polling github for the set of merge candidate branches,
// then repeatedly prunes these and fast-forwards the main branch until a steady
// state is reached.
// At this point it tries to enrich the set of merge candidate branches by
// polling github for pull requests which have recently been marked either as
// mergeable (by commenting "bors r+") or cancellable (with "bors r-").
// The terminal state is reached if no additional branches were created.
func StateMachine(c GithubClient, commentLookback time.Duration) {
	for {
		var s State
		for {
			s = FetchMergeCandidateBranchState(c)
			t := s.BuildPipelineTree()
			s = s.ToPrunedOrphanedBranches(c, t)
			ff := s.FindFastForward(t)
			if ff == nil {
				break
			}
			c.FastForwardBase(*ff)
		}
		s = s.ToDecoratedWithPullRequests(c, commentLookback)
		s = s.ToPrunedCancelledPullRequests(c)
		pr := s.NextMergeablePullRequest()
		if pr == 0 {
			break
		}
		t := s.BuildPipelineTree()
		s.CreateBranchesForPullRequest(c, t, pr)
	}
}

func main() {
	owner := os.Args[1]
	repo := os.Args[2]
	baseBranch := os.Args[3]
	oauth2Token := os.Args[4]
	commentsSinceStr := os.Args[5]
	commentsSince, err := time.ParseDuration(commentsSinceStr)
	if err != nil {
		panic(err)
	}
	c := NewGithubClient(owner, repo, baseBranch, oauth2Token)
	StateMachine(c, commentsSince)
}
