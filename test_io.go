package main

import (
	"sort"
	"testing"
)

// TestInputBranchValue defines the state of a merge candidate branch at the
// beginning of a test case.
type TestInputBranchValue struct {
	// ParentBranch identifies the non-pull-request parent branch.
	// A merge candidate branch commit only ever has one or two parents:
	// 1. the head of either the base branch or another merge candidate branch,
	// 2. possibly the head of a pull request branch.
	ParentBranch string `yaml:"parent_branch"`
	// NoPullRequestParent indicates that there is no pull request parent.
	NoPullRequestParent bool `yaml:"no_pr_parent,omitempty"`
	// CheckPass indicates whether the check suite passed, nil if pending.
	CheckPass *bool `yaml:"check_pass,omitempty"`
	// PullRequestConflicts defines the set of pull requests which would have a
	// merge conflict with this branch.
	PullRequestConflicts []int `yaml:"pr_conflicts,flow,omitempty"`
}

// TestCaseInput contains the state of the github repo at the beginning of a
// test case.
type TestCaseInput struct {
	// Branches contains the state of the merge candidate branches.
	Branches map[string]TestInputBranchValue `yaml:"branches,omitempty"`
	// PassingCommits maps the number of iterations after which a check suite
	// will pass for a given CommitID.
	PassingCommits map[string]uint `yaml:"passing_commits,omitempty"`
	// FailingCommits maps the number of iterations after which a check suite
	// will pass for a given CommitID.
	FailingCommits map[string]uint `yaml:"failing_commits,omitempty"`
	// MergeablePullRequests holds the comments for mergeable pull requests.
	MergeablePullRequests map[int][]string `yaml:"mergeable_prs,omitempty"`
	// MergeablePullRequests holds the comments for unmergeable pull requests.
	UnmergeablePullRequests map[int][]string `yaml:"unmergeable_prs,omitempty"`
}

// testBaseHead is the name of the commit at the head of the base branch
// at the beginning of the test case.
const testBaseHead string = "main"

// NewTestGithubClient builds a TestGithubClient based off the input of a test
// case.
func (tc TestCaseInput) NewTestGithubClient(t *testing.T) TestGithubClient {
	ts := TestState{
		baseHead:       CommitID(testBaseHead),
		branches:       map[BranchKey]BranchValue{},
		mergeConflicts: map[TestMergeConflict]struct{}{},
		pullRequests:   map[PullRequestNumber]TestPullRequest{},
		comments:       []TestComment{},
		passingCommits: map[CommitID]uint{},
		failingCommits: map[CommitID]uint{},
	}

	// Add pull requests and comments.
	addPRAndComments := func(numberInt int, isMergeable bool, comments []string) {
		number := PullRequestNumber(numberInt)
		ts.pullRequests[number] = TestPullRequest{
			PullRequestNumber: number,
			CommitID:          testPRCommitID(number),
			isMergeable:       isMergeable,
		}
		for _, comment := range comments {
			ts.comments = append(ts.comments, TestComment{
				PullRequestNumber: number,
				msg:               comment,
			})
		}
	}
	for number, comments := range tc.MergeablePullRequests {
		addPRAndComments(number, true, comments)
	}
	for number, comments := range tc.UnmergeablePullRequests {
		addPRAndComments(number, false, comments)
	}

	// Add passing and failing commits.
	for sha, counter := range tc.PassingCommits {
		ts.passingCommits[CommitID(sha)] = counter
	}
	for sha, counter := range tc.FailingCommits {
		ts.failingCommits[CommitID(sha)] = counter
	}

	// Add branches and merge conflicts.
	branchParent := map[BranchKey]BranchKey{}
	for k, v := range tc.Branches {
		bk, ok := ParseBranchKey(k)
		if !ok {
			t.Fatalf("invalid branch name %s", k)
		}
		for _, numberInt := range v.PullRequestConflicts {
			ts.mergeConflicts[TestMergeConflict{
				BranchKey:         bk,
				PullRequestNumber: PullRequestNumber(numberInt),
			}] = struct{}{}
		}
		if v.ParentBranch == testBaseHead {
			branchParent[bk] = BranchKey{}
		} else if pbk, ok := ParseBranchKey(v.ParentBranch); ok {
			branchParent[bk] = pbk
		} else {
			t.Fatalf("invalid parent branch name %s", v.ParentBranch)
		}
		bv := BranchValue{
			CommitID:    "",
			Parents:     []CommitID{},
			isValid:     !v.NoPullRequestParent,
			IsCheckDone: false,
			IsCheckPass: false,
		}
		if v.CheckPass != nil {
			bv.IsCheckDone = true
			bv.IsCheckPass = *v.CheckPass
		}
		ts.branches[bk] = bv
	}

	// Build commit graph.
	toCommit := map[BranchKey]CommitID{}
	toCommit[BranchKey{}] = ts.baseHead
	for {
		flag := false
		for bk, bv := range ts.branches {
			if len(bv.Parents) > 0 {
				continue
			}
			bkParent := branchParent[bk]
			shaParent, ok := toCommit[branchParent[bk]]
			if !ok {
				continue
			}
			if bv.isValid {
				shaPR := testPRCommitID(bk.PullRequestNumber)
				bv.CommitID = testMergeCommitID(shaParent, shaPR)
				bv.Parents = []CommitID{shaParent, shaPR}
			} else {
				bvParent := ts.branches[bkParent]
				if bv.CommitID != shaParent {
					t.Fatal("internal error")
				}
				bv.CommitID = bvParent.CommitID
				bv.Parents = append(bv.Parents, bvParent.Parents...)
			}
			toCommit[bk] = bv.CommitID
			ts.branches[bk] = bv
			flag = true
		}
		if !flag {
			break
		}
	}
	for bk, bv := range ts.branches {
		if len(bv.CommitID) == 0 {
			t.Fatalf("could not infer commit for branch %s", bk.BranchName())
		}
	}

	return TestGithubClient{T: t, TestState: ts}
}

// TestOutputBranchValue defines the final state of a merge candidate branch.
type TestOutputBranchValue struct {
	// Head is the commit at the head of the branch.
	Head string `yaml:"head"`
	// Parents are the parent commits of the head of the branch.
	Parents []string `yaml:"parents"`
	// CheckPass is true if the check suite passed, false if it failed, nil if
	// it hasn't completed yet.
	CheckPass *bool `yaml:"check_pass,omitempty"`
}

// TestCaseOutput contains the state of the github repo at the end of a test
// case.
type TestCaseOutput struct {
	// BaseHead is the final state of the base branch.
	BaseHead string `yaml:"base_head"`
	// MergeablePullRequests is the final set of mergeable pull requests.
	MergeablePullRequests []int `yaml:"mergeable_prs,flow,omitempty"`
	// UnmergeablePullRequests is the final set of unmergeable pull requests.
	UnmergeablePullRequests []int `yaml:"unmergeable_prs,flow,omitempty"`
	// Branches is the final set of merge candidate branches.
	Branches map[string]TestOutputBranchValue `yaml:"branches,omitempty"`
	// ApiTrace contains a sequential trace of all github API calls which
	// changed the state of the github repo.
	ApiTrace []string `yaml:"api_trace,omitempty"`
}

// ToTestCaseOutput transforms the TestState into a serializable TestCaseOutput.
func (ts TestState) ToTestCaseOutput() TestCaseOutput {
	tco := TestCaseOutput{
		BaseHead:                string(ts.baseHead),
		Branches:                map[string]TestOutputBranchValue{},
		MergeablePullRequests:   []int{},
		UnmergeablePullRequests: []int{},
		ApiTrace:                append([]string{}, ts.apiTrace...),
	}
	for bk, bv := range ts.branches {
		tobv := TestOutputBranchValue{
			Head:      string(bv.CommitID),
			Parents:   make([]string, len(bv.Parents)),
			CheckPass: nil,
		}
		for i, p := range bv.Parents {
			tobv.Parents[i] = string(p)
		}
		if bv.IsCheckDone {
			value := bv.IsCheckPass
			tobv.CheckPass = &value
		}
		tco.Branches[bk.BranchName()] = tobv
	}
	for number, pr := range ts.pullRequests {
		if pr.isMergeable {
			tco.MergeablePullRequests = append(tco.MergeablePullRequests, int(number))
		} else {
			tco.UnmergeablePullRequests = append(tco.UnmergeablePullRequests, int(number))
		}
	}
	sort.Ints(tco.MergeablePullRequests)
	sort.Ints(tco.UnmergeablePullRequests)
	return tco
}
