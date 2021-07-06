package main

import (
	"context"
	"github.com/google/go-github/v36/github"
	"golang.org/x/oauth2"
	"strconv"
	"strings"
	"time"
)

const perPage = 100
const mergeConflictStatusCode = 409
const notFoundStatusCode = 404

// githubClientImpl implements GithubClient using the actual github HTTP REST
// API, wrapped by the go-github package.
//
// The HTTP calls are all synchronous and could obviously be made concurrent but
// I just haven't bothered doing that yet.
type githubClientImpl struct {
	*github.Client
	owner, repo, baseBranchName, token string
}

var _ GithubClient = (*githubClientImpl)(nil)

func NewGithubClient(owner, repo, baseBranchName, token string) GithubClient {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(context.Background(), ts)
	return &githubClientImpl{
		Client:         github.NewClient(tc),
		owner:          owner,
		repo:           repo,
		baseBranchName: baseBranchName,
		token:          token,
	}
}

func (c *githubClientImpl) GetBranch(bk BranchKey) BranchValue {
	b, _, err := c.Repositories.GetBranch(context.Background(), c.owner, c.repo, bk.BranchName())
	onErrPanic(err)
	bv := BranchValue{
		CommitID:    CommitID(b.GetCommit().GetSHA()),
		Parents:     make([]CommitID, len(b.GetCommit().Parents)),
		isValid:     true,
		IsCheckDone: false,
		IsCheckPass: false,
	}
	for i, p := range b.GetCommit().Parents {
		bv.Parents[i] = CommitID(p.GetSHA())
	}
	fromCommit, ok := ParseBranchKey(b.GetCommit().GetCommit().GetMessage())
	if !ok || fromCommit != bk {
		bv.isValid = false
		return bv
	}
	opts := &github.ListCheckSuiteOptions{
		ListOptions: github.ListOptions{Page: 1, PerPage: perPage},
	}
	flagAtLeastOne := false
	flagIncomplete := false
	flagFailed := false
	for {
		suites, resp, err := c.Checks.ListCheckSuitesForRef(context.Background(), c.owner, c.repo, b.GetCommit().GetSHA(), opts)
		onErrPanic(err)
		for _, s := range suites.CheckSuites {
			flagAtLeastOne = true
			if s.GetStatus() != "completed" {
				flagIncomplete = true
			} else if s.GetConclusion() != "success" {
				flagFailed = true
			}
		}
		if flagFailed || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if flagFailed {
		bv.IsCheckDone = true
	} else if flagAtLeastOne && !flagIncomplete {
		bv.IsCheckDone = true
		bv.IsCheckPass = true
	}
	return bv
}

func (c *githubClientImpl) CreateBranch(bk BranchKey, sha CommitID) {
	ref := &github.Reference{
		Ref:    github.String("refs/heads/" + bk.BranchName()),
		Object: &github.GitObject{SHA: github.String(string(sha))},
	}
	_, _, err := c.Git.CreateRef(context.Background(), c.owner, c.repo, ref)
	onErrPanic(err)
}

func (c *githubClientImpl) DeleteBranch(bk BranchKey) {
	_, err := c.Git.DeleteRef(context.Background(), c.owner, c.repo, "heads/"+bk.BranchName())
	onErrPanic(err)
}

func (c *githubClientImpl) MergeBranch(bk BranchKey, sha CommitID) bool {
	req := &github.RepositoryMergeRequest{
		Base:          github.String(bk.BranchName()),
		Head:          github.String(string(sha)),
		CommitMessage: github.String(bk.BranchName()),
	}
	_, resp, err := c.Repositories.Merge(context.Background(), c.owner, c.repo, req)
	if err != nil {
		if resp != nil && resp.StatusCode == mergeConflictStatusCode {
			return false
		}
		onErrPanic(err)
	}
	return true
}

func (c *githubClientImpl) GetBaseHead() CommitID {
	base, _, err := c.Repositories.GetBranch(context.Background(), c.owner, c.repo, c.baseBranchName)
	onErrPanic(err)
	return CommitID(base.GetCommit().GetSHA())
}

func (c *githubClientImpl) FastForwardBase(sha CommitID) {
	ref := &github.Reference{
		Ref:    github.String("refs/heads/" + c.baseBranchName),
		Object: &github.GitObject{SHA: github.String(string(sha))},
	}
	_, _, err := c.Git.UpdateRef(context.Background(), c.owner, c.repo, ref, false)
	onErrPanic(err)
}

func (c *githubClientImpl) GetMergeablePullRequestHead(number PullRequestNumber) *CommitID {
	for {
		pr, resp, err := c.PullRequests.Get(context.Background(), c.owner, c.repo, int(number))
		if err != nil {
			if resp != nil && resp.StatusCode == notFoundStatusCode {
				return nil
			}
			onErrPanic(err)
		}
		if pr.GetState() != "open" || pr.GetLocked() || pr.GetDraft() {
			return nil
		}
		if pr.Mergeable == nil {
			// Wait for github to determine if PR can be merged or not.
			time.Sleep(time.Second)
			continue
		}
		if !pr.GetMergeable() {
			return nil
		}
		ret := CommitID(pr.GetHead().GetSHA())
		return &ret
	}
}

func (c *githubClientImpl) ListAllCommentsSince(duration time.Duration, fn func(number PullRequestNumber, msg string)) {
	since := time.Now().Add(-duration)
	opts := &github.IssueListCommentsOptions{
		Sort:        github.String("created"),
		Direction:   github.String("asc"),
		Since:       &since,
		ListOptions: github.ListOptions{Page: 1, PerPage: perPage},
	}
	for {
		comments, resp, err := c.Issues.ListComments(context.Background(), c.owner, c.repo, 0, opts)
		onErrPanic(err)
		for _, comment := range comments {
			components := strings.Split(comment.GetIssueURL(), "/")
			num, err := strconv.Atoi(components[len(components)-1])
			onErrPanic(err)
			fn(PullRequestNumber(num), comment.GetBody())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page++
	}
}

func (c *githubClientImpl) ListAllMergeCandidateBranches(fn func(bk BranchKey)) {
	opts := &github.BranchListOptions{
		ListOptions: github.ListOptions{Page: 1, PerPage: perPage},
	}
	for {
		results, resp, err := c.Repositories.ListBranches(context.Background(), c.owner, c.repo, opts)
		onErrPanic(err)
		for _, result := range results {
			bk, ok := ParseBranchKey(result.GetName())
			if ok {
				fn(bk)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
}

func onErrPanic(err error) {
	if err != nil {
		panic(err)
	}
}
