package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MergeCandidateBranchPrefix is the prefix in a branch name which identifies
// it as a merge candidate branch.
const MergeCandidateBranchPrefix = "merge-candidate"

// PullRequestNumber uniquely identifies a pull request.
type PullRequestNumber int

// CommitID uniquely identifies a commit.
type CommitID string

// BranchKey uniquely identifies a merge candidate branch.
type BranchKey struct {
	PipelineCounter   int
	PullRequestNumber PullRequestNumber
}

// BranchValue stores all the necessary data for a merge candidate branch.
type BranchValue struct {
	CommitID
	Parents     []CommitID
	isValid     bool
	IsCheckDone bool
	IsCheckPass bool
}

// GithubClient is the interface for the parts of the github API which we need.
// Any errors will result in a panic.
type GithubClient interface {

	// GetBranch gets detailed data on the state of an existing merge candidate
	// branch, including its check suite status.
	GetBranch(bk BranchKey) BranchValue

	// CreateBranch creates a new merge candidate branch at the specified
	// commit.
	CreateBranch(bk BranchKey, sha CommitID)

	// DeleteBranch deletes an existing merge candidate branch.
	DeleteBranch(bk BranchKey)

	// MergeBranch attempts to merge an existing commit into an existing merge
	// candidate branch. Returns true iff the merge succeeds.
	MergeBranch(bk BranchKey, sha CommitID) bool

	// GetBaseHead returns the commit at the head to the base branch, in which
	// all merge candidate branches are based off (directly or indirectly).
	GetBaseHead() CommitID

	// FastForwardBase fast-forwards the base branch to the specified commit.
	FastForwardBase(sha CommitID)

	// GetMergeablePullRequestHead returns the commit at the head of the pull
	// request with the specified number, if it exists, and if it is mergeable:
	// open, not locked, etc. Returns nil otherwise.
	GetMergeablePullRequestHead(number PullRequestNumber) *CommitID

	// ListAllCommentsSince fetches all issue comments created up to a certain
	// duration of time ago, and applies the provided function to each of their
	// contents.
	ListAllCommentsSince(duration time.Duration, fn func(number PullRequestNumber, msg string))

	// ListAllMergeCandidateBranches fetches all branch names and applies the
	// provided function to each merge candidate branch key.
	ListAllMergeCandidateBranches(fn func(bk BranchKey))
}

// BranchName returns the merge candidate branch name for this BranchKey.
func (bk BranchKey) BranchName() string {
	return fmt.Sprintf("%s-%d-%d", MergeCandidateBranchPrefix, bk.PullRequestNumber, bk.PipelineCounter)
}

// ParseBranchKey extracts a BranchKey from a merge candidate branch name.
func ParseBranchKey(branchName string) (bk BranchKey, isValid bool) {
	if !strings.HasPrefix(branchName, MergeCandidateBranchPrefix+"-") {
		return bk, false
	}
	suffix := branchName[len(MergeCandidateBranchPrefix+"-"):]
	parts := strings.Split(suffix, "-")
	if len(parts) != 2 {
		return bk, false
	}
	num, err := strconv.Atoi(parts[0])
	bk.PullRequestNumber = PullRequestNumber(num)
	if err != nil || bk.PullRequestNumber <= 0 {
		return bk, false
	}
	bk.PipelineCounter, err = strconv.Atoi(parts[1])
	if err != nil || bk.PipelineCounter <= 0 {
		return bk, false
	}
	return bk, true
}
