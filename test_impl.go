package main

import (
	"fmt"
	"testing"
	"time"
)

type TestMergeConflict struct {
	BranchKey
	PullRequestNumber
}

type TestComment struct {
	PullRequestNumber
	msg string
}

type TestPullRequest struct {
	PullRequestNumber
	CommitID
	isMergeable bool
}

// TestState mocks the state of the github repo.
type TestState struct {
	baseHead       CommitID
	branches       map[BranchKey]BranchValue
	mergeConflicts map[TestMergeConflict]struct{}
	pullRequests   map[PullRequestNumber]TestPullRequest
	comments       []TestComment
	passingCommits map[CommitID]uint
	failingCommits map[CommitID]uint
	apiTrace       []string
}

// TestGithubClient implements GithubClient for tests.
type TestGithubClient struct {
	*testing.T
	TestState
}

var _ GithubClient = (*TestGithubClient)(nil)

func (t *TestGithubClient) GetBranch(bk BranchKey) BranchValue {
	t.checkBranchExistence(bk)
	bv := t.branches[bk]
	if !bv.IsCheckDone {
		if counter, ok := t.passingCommits[bv.CommitID]; ok {
			if counter <= 1 {
				bv.IsCheckDone = true
				bv.IsCheckPass = true
				t.trace("checks pass for %s", bk.BranchName())
			}
			t.passingCommits[bv.CommitID] = counter - 1
		}
		if counter, ok := t.failingCommits[bv.CommitID]; ok {
			if counter <= 1 {
				bv.IsCheckDone = true
				bv.IsCheckPass = false
				t.trace("checks fail for %s", bk.BranchName())
			}
			t.failingCommits[bv.CommitID] = counter - 1
		}
		t.branches[bk] = bv
	}
	return bv
}

func (t *TestGithubClient) CreateBranch(bk BranchKey, sha CommitID) {
	t.trace("create %s at %s", bk.BranchName(), sha)
	t.checkBranchNonExistence(bk)
	t.checkCommitExistence(sha)
	if sha == t.baseHead {
		bv := BranchValue{
			CommitID: t.baseHead,
			Parents:  []CommitID{},
			isValid:  true,
		}
		t.branches[bk] = bv
	}
	for _, bv := range t.branches {
		if bv.CommitID == sha {
			bv.Parents = append([]CommitID{}, bv.Parents...)
			bv.isValid = false
			t.branches[bk] = bv
			return
		}
	}
	t.Fatalf("commit %s not found in merge candidate branch set", sha)
}

func (t *TestGithubClient) DeleteBranch(bk BranchKey) {
	t.trace("delete %s", bk.BranchName())
	t.checkBranchExistence(bk)
	delete(t.branches, bk)
}

func (t *TestGithubClient) MergeBranch(bk BranchKey, sha CommitID) bool {
	t.trace("merge %s into %s", sha, bk.BranchName())
	t.checkBranchExistence(bk)
	t.checkCommitExistence(sha)
	number := t.findMergeablePullRequest(sha)
	if number != bk.PullRequestNumber {
		t.Fatalf("branch is %s but merged commit %s is from #%d", bk.BranchName(), sha, number)
	}
	bv := t.branches[bk]
	_, isConflict := t.mergeConflicts[TestMergeConflict{BranchKey: bk, PullRequestNumber: number}]
	if isConflict {
		t.branches[bk] = BranchValue{
			CommitID: bv.CommitID,
			Parents:  append([]CommitID{}, bv.Parents...),
		}
		return false
	}
	t.branches[bk] = BranchValue{
		CommitID:    testMergeCommitID(bv.CommitID, sha),
		Parents:     []CommitID{bv.CommitID, sha},
		isValid:     true,
		IsCheckDone: false,
		IsCheckPass: false,
	}
	return true
}

func (t *TestGithubClient) GetBaseHead() CommitID {
	return t.baseHead
}

func (t *TestGithubClient) FastForwardBase(sha CommitID) {
	t.trace("fast-forward to %s", sha)
	t.checkCommitExistence(sha)
	bkTarget, bvTarget := t.findBranch(sha)
	for t.baseHead != sha {
		ff := t.walkBackToBase(bkTarget, bvTarget)
		if ff == nil {
			t.Fatalf("fast-forward target %s is not linked to base %s", sha, t.baseHead)
		}
		bk := *ff
		bv := t.branches[bk]
		for _, p := range bv.Parents {
			if p == t.baseHead {
				continue
			}
			// Mark PR as merged.
			number := t.findMergeablePullRequest(p)
			pr := t.pullRequests[number]
			pr.isMergeable = false
			t.pullRequests[number] = pr
		}
		t.baseHead = bv.CommitID
	}
}

func (t *TestGithubClient) GetMergeablePullRequestHead(number PullRequestNumber) *CommitID {
	pr, ok := t.pullRequests[number]
	if !ok || !pr.isMergeable {
		return nil
	}
	return &pr.CommitID
}

func (t *TestGithubClient) ListAllCommentsSince(_ time.Duration, fn func(number PullRequestNumber, msg string)) {
	for _, tc := range t.comments {
		fn(tc.PullRequestNumber, tc.msg)
	}
}

func (t *TestGithubClient) ListAllMergeCandidateBranches(fn func(bk BranchKey)) {
	for bk := range t.branches {
		fn(bk)
	}
}

func (t *TestGithubClient) checkBranchExistence(bk BranchKey) {
	_, ok := t.branches[bk]
	if !ok {
		t.Fatalf("branch %s not found", bk.BranchName())
	}
}

func (t *TestGithubClient) checkBranchNonExistence(bk BranchKey) {
	_, ok := t.branches[bk]
	if ok {
		t.Fatalf("branch %s already exists", bk.BranchName())
	}
}

func (t *TestGithubClient) checkCommitExistence(sha CommitID) {
	if t.baseHead == sha {
		return
	}
	for _, bv := range t.branches {
		if bv.CommitID == sha {
			return
		}
	}
	for _, pr := range t.pullRequests {
		if pr.CommitID == sha {
			return
		}
	}
	t.Fatalf("commit %s not found", sha)
}

func (t *TestGithubClient) findMergeablePullRequest(sha CommitID) PullRequestNumber {
	for number, pr := range t.pullRequests {
		if pr.CommitID == sha {
			if !pr.isMergeable {
				t.Fatalf("commit %s belongs to #%d which is not mergeable", sha, number)
			}
			return number
		}
	}
	t.Fatalf("commit %s not found in pull request set", sha)
	return 0
}

func (t *TestGithubClient) findBranch(sha CommitID) (BranchKey, BranchValue) {
	for bk, bv := range t.branches {
		if bv.CommitID == sha {
			return bk, bv
		}
	}
	t.Fatalf("commit %s not found in merge candidate branch set", sha)
	return BranchKey{}, BranchValue{}
}

func (t *TestGithubClient) walkBackToBase(bk BranchKey, bv BranchValue) *BranchKey {
	for _, p := range bv.Parents {
		if p == t.baseHead {
			return &bk
		}
	}
	for bkNext, bvNext := range t.branches {
		isParent := false
		for _, p := range bv.Parents {
			if p == bvNext.CommitID {
				isParent = true
				break
			}
		}
		if !isParent {
			continue
		}
		maybeResult := t.walkBackToBase(bkNext, bvNext)
		if maybeResult != nil {
			return maybeResult
		}
	}
	return nil
}

func (t *TestGithubClient) trace(fmtstr string, args ...interface{}) {
	t.apiTrace = append(t.apiTrace, fmt.Sprintf(fmtstr, args...))
}

func testMergeCommitID(a, b CommitID) CommitID {
	return CommitID(fmt.Sprintf("merge(%s, %s)", a, b))
}

func testPRCommitID(number PullRequestNumber) CommitID {
	return CommitID(fmt.Sprintf("pr-%d", number))
}
