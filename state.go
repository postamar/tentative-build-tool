package main

import (
	"regexp"
	"strings"
	"time"
)

// State stores the current state in the state machine.
type State struct {
	// Base is the commit at the head of the base branch, which is the branch
	// which we're trying to fast-forward using merge candidate branches with
	// passing check suites.
	Base CommitID
	// Branches is the current set of merge candidate branches.
	Branches map[BranchKey]BranchValue
	// MergeablePullRequests is the current set of pull requests which could
	// be merged: open, not locked, not drafts, no merge conflicts, etc.
	// Note that this does not take check suites into account.
	// Most importantly, the pull request must have a "bors r+" or "bors merge"
	// comment.
	// The map value is the commit at the head of the pull request branch.
	MergeablePullRequests map[PullRequestNumber]CommitID
	// CancelledPullRequests is the current set of pull requests for which
	// a cancellation order has been emitted ("bors cancel" or "bors r-"), and
	// which has not been superseded by a subsequent "bors r+" or "bors merge"
	// comment.
	CancelledPullRequests map[PullRequestNumber]struct{}
}

// PipelineValue is used to define PipelineTree and encodes the position of the
// corresponding merge candidate branch in the build pipeline.
type PipelineValue struct {
	// Predecessor is the predecessor branch in the build pipeline.
	// A zero value indicates the base branch.
	Predecessor BranchKey
	// Weight is the distance of this merge candidate branch from the head of
	// the base branch, in numbers of commits.
	Weight int
	// IsNotInPipeline is a tombstone to indicate that the branch should not be
	// considered as being in the build pipeline any more. This tombstone gets
	// set when the check suites fail for a branch, for instance.
	IsNotInPipeline bool
}

// PipelineTree is a data structure derived from a state which materializes
// the build pipeline tree, that is to say the subset of merge candidate
// branches for which the base branch might conceivably be fast-forwarded to,
// irrespective of their check suite status.
type PipelineTree map[BranchKey]PipelineValue

// FetchMergeCandidateBranchState initializes a state with the set of merge
// candidate branches.
func FetchMergeCandidateBranchState(c GithubClient) State {
	ns := fresh(State{})
	ns.Base = c.GetBaseHead()
	c.ListAllMergeCandidateBranches(func(bk BranchKey) {
		ns.Branches[bk] = c.GetBranch(bk)
	})
	return ns
}

// BuildPipelineTree builds a PipelineTree based off the current state.
// See the PipelineTree type definition for more details.
func (os State) BuildPipelineTree() PipelineTree {
	t := make(PipelineTree, len(os.Branches))
	shas := make(map[CommitID]BranchKey, len(os.Branches))
	shas[os.Base] = BranchKey{}
	for {
		numAdded := 0
		for bk, bv := range os.Branches {
			if _, found := t[bk]; found {
				continue
			}
			isInTree := false
			var parentKey BranchKey
			for _, p := range bv.Parents {
				parentKey, isInTree = shas[p]
				if isInTree {
					break
				}
			}
			if !isInTree {
				continue
			}
			numAdded++
			shas[bv.CommitID] = bk
			t[bk] = PipelineValue{
				Predecessor:     parentKey,
				IsNotInPipeline: t[parentKey].IsNotInPipeline || !bv.isValid || (bv.IsCheckDone && !bv.IsCheckPass),
				Weight:          t[parentKey].Weight + 1,
			}
		}
		if numAdded == 0 {
			break
		}
	}
	return t
}

// ToPrunedOrphanedBranches transitions the state to another in which all
// orphaned merge candidate branches have been pruned.
func (os State) ToPrunedOrphanedBranches(c GithubClient, t PipelineTree) State {
	ns := deepCopy(os)
	for bk := range ns.Branches {
		if _, ok := t[bk]; !ok {
			c.DeleteBranch(bk)
			delete(ns.Branches, bk)
		}
	}
	return ns
}

// FindFastForward identifies a commit to fast-foward to.
// Returns nil if none was found.
// There are several possible heuristics here, we chose to pick the one which
// is in the longest pipeline path.
func (os State) FindFastForward(t PipelineTree) *CommitID {
	pipelineHead := BranchKey{}
	for bk, pv := range t {
		if pv.IsNotInPipeline {
			continue
		}
		if t[pipelineHead].Weight < pv.Weight {
			pipelineHead = bk
		} else if t[pipelineHead].Weight == pv.Weight && pipelineHead.PullRequestNumber > bk.PullRequestNumber {
			pipelineHead = bk
		}
	}
	for {
		bv, ok := os.Branches[pipelineHead]
		if !ok {
			return nil
		}
		if bv.IsCheckPass {
			return &bv.CommitID
		}
		pipelineHead = t[pipelineHead].Predecessor
	}
}

// ToDecoratedWithPullRequests transitions the state to another which is
// decorated with data on mergeable and canellable pull requests, based off
// the set of merge candidate branches as well as all recently-created issue
// comments with the lines "bors merge" or "bors cancel" which mark the pull
// requests as to be merged or as to cancel an ongoing merge attempt,
// respectively.
func (os State) ToDecoratedWithPullRequests(c GithubClient, commentsSince time.Duration) State {
	borsMergeRe, err := regexp.Compile(`^\s*bors\s+(r\+|r=.*|merge|merge=.*)\s*$`)
	if err != nil {
		panic(err)
	}
	borsCancelRe, err := regexp.Compile(`^\s*bors\s+(r-|merge-|cancel)\s*$`)
	if err != nil {
		panic(err)
	}
	ns := deepCopy(os)

	numbers := make(map[PullRequestNumber]bool)
	for bk := range ns.Branches {
		numbers[bk.PullRequestNumber] = false
	}
	c.ListAllCommentsSince(commentsSince, func(number PullRequestNumber, msg string) {
		for _, line := range strings.Split(msg, "\n") {
			if borsMergeRe.MatchString(line) {
				numbers[number] = false
			}
			if borsCancelRe.MatchString(line) {
				numbers[number] = true
			}
		}
	})

	for number, isCancelled := range numbers {
		if isCancelled {
			ns.CancelledPullRequests[number] = struct{}{}
		} else {
			maybeCommitID := c.GetMergeablePullRequestHead(number)
			if maybeCommitID != nil {
				ns.MergeablePullRequests[number] = *maybeCommitID
			}
		}
	}

	return ns
}

// ToPrunedCancelledPullRequests transitions the state to another in which
// the merge candidate branches for cancelled pull requests have been deleted.
func (os State) ToPrunedCancelledPullRequests(c GithubClient) State {
	ns := deepCopy(os)
	for bk := range ns.Branches {
		if _, ok := ns.CancelledPullRequests[bk.PullRequestNumber]; ok {
			c.DeleteBranch(bk)
			delete(ns.Branches, bk)
		}
	}
	ns.CancelledPullRequests = nil
	return ns
}

// NextMergeablePullRequest returns the number of a pull request for which
// merge candidate branches could be created. Returns 0 if none is available.
// There are several possible heuristics here, we chose to pick the one with
// the smallest number, as this often corresponds to the oldest pull request.
func (os State) NextMergeablePullRequest() PullRequestNumber {
	numbersInBranches := map[PullRequestNumber]struct{}{}
	for bk := range os.Branches {
		numbersInBranches[bk.PullRequestNumber] = struct{}{}
	}
	var nextNumber PullRequestNumber
	for number := range os.MergeablePullRequests {
		if _, found := numbersInBranches[number]; found {
			continue
		}
		if nextNumber == 0 || nextNumber > number {
			nextNumber = number
		}
	}
	return nextNumber
}

// CreateBranchesForPullRequest transitions the state to another (implicit)
// state in which new merge candidate branches have been created for a mergeable
// pull request.
// There are several possible heuristics here, we chose to create branches based
// off of all commits in the build pipeline tree, as well as a branch off of the
// the base branch.
func (os State) CreateBranchesForPullRequest(c GithubClient, t PipelineTree, number PullRequestNumber) {
	bk := BranchKey{
		PullRequestNumber: number,
		PipelineCounter:   1,
	}
	pullRequestHead, ok := os.MergeablePullRequests[bk.PullRequestNumber]
	if !ok {
		return
	}
	c.CreateBranch(bk, os.Base)
	c.MergeBranch(bk, pullRequestHead)
	for pk, pv := range t {
		if pv.IsNotInPipeline {
			continue
		}
		bk.PipelineCounter++
		c.CreateBranch(bk, os.Branches[pk].CommitID)
		c.MergeBranch(bk, pullRequestHead)
	}
}

// fresh returns an empty state, with memory pre-allocated according to the
// given state
func fresh(other State) State {
	return State{
		Branches:              make(map[BranchKey]BranchValue, len(other.Branches)),
		MergeablePullRequests: make(map[PullRequestNumber]CommitID, len(other.MergeablePullRequests)),
		CancelledPullRequests: make(map[PullRequestNumber]struct{}, len(other.CancelledPullRequests)),
	}
}

// deepCopy returns a deep copy of the given state.
func deepCopy(other State) State {
	ns := fresh(other)
	ns.Base = other.Base
	for bk, bv := range other.Branches {
		nbv := bv
		nbv.Parents = append(make([]CommitID, 0, len(bv.Parents)), bv.Parents...)
		ns.Branches[bk] = nbv
	}
	for number, c := range other.MergeablePullRequests {
		ns.MergeablePullRequests[number] = c
	}
	for number := range other.CancelledPullRequests {
		ns.CancelledPullRequests[number] = struct{}{}
	}
	return ns
}
