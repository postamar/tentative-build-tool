# A build tool experiment

### Problem

This little experiment originated from the following problem. At Cockroach Labs we use [bors-ng](https://github.com/bors-ng/bors-ng) to merge our PRs into the main branch. It works fine and there's nothing wrong with it per se, however sometimes we struggle because of it due to some undesirable aspects of our software lifecyle:
1. We sometimes experience bursts of activity, as an artifact of our release cycle, in which we can be merging a dozen PRs concurrently. This is a fine use-case for bors or any such tool which prevents merge skews.
2. However, our CI run durations are quite long, anywhere from a half-hour to an hour, and we can expect that to grow.
3. Compounding this, some of our tests turn out to be flaky.

The bad situation we can end up in is bors tries to merge a batch with a dozen PRs and fails; bors then breaks up this batch into smaller batches which eventually get merged but meanwhile the backlog of PRs grows faster than bors's ability to merge them.

### Idea

Instead of the batching strategy that bors uses, in our case we might be better served by a pipelining strategy, allowing CI to run concurrently.
Consider the following example involving two PRs, `PR#1` and `PR#2` which we wish to add to the main branch after the current `HEAD`.
Assume henceforth for the sake of simplicity that each build always takes an hour.

Bors would run the following builds in sequence:
1. At 00:00 the build for `merge(HEAD, PR#1, PR#2)` kicks off,
2. Assuming it fails, at 01:00 the build for `merge(HEAD, PR#1)` kicks off,
3. and then at 02:00 either `merge(HEAD, PR#2)` or `merge(merge(HEAD, PR#1), PR#2)` kick off, depending if the second build succeeds or fails.

Pipelining would instead involve running the following builds concurrently:
- `merge(HEAD, PR#1)`,
- `merge(merge(HEAD, PR#1), PR#2)`, which speculatively assumes `merge(HEAD, PR#1)` will succeed,
- `merge(HEAD, PR#2)`, which speculatively assumes the opposite.

The following sequence of events becomes possible:
1. At 00:00, `PR#1` is submitted for merge, kicking off a build for `merge(HEAD, PR#1)`.
2. At 00:30, `PR#2` is submitted for merge, kicking off builds for `merge(merge(HEAD, PR#1), PR#2)` and `merge(HEAD, PR#2)`.
3. At 01:00, the build for `merge(HEAD, PR#1)` succeeds, we fast-forward the main branch to that commit, and discard the `merge(HEAD, PR#2)` build. 
4. At 01:30 the build for `merge(merge(HEAD, PR#1), PR#2)` succeeds, and we fast-forward the main branch to that commit.

By treating PRs submitted for merge in a FIFO manner we can fast-forward the main branch without invalidating all concurrent builds.
Alternatively, suppose the first build fails, the sequence of events becomes:
3. At 01:00, the build for `merge(HEAD, PR#1)` fails, we discard it as well as the `merge(merge(HEAD, PR#1), PR#2)` build.
4. At 01:30 the build for `merge(HEAD, PR#2)` succeeds, and we fast-forward the main branch to that commit.
5. We may want to kick off builds for `PR#1` again, or not.

Such a pipelining strategy is more time-efficient that the batching strategy, at the expense of additional computing resources for CI runs.
It also has the nice property of keeping the master branch in a fresher state. 

### Implementation and future work

I got this small proof-of-concept working, which maintains the build state entirely in the github repo, and accesses it by polling the github API.
Builds are kicked off as branches prefixed with `merge-candidate-` and whether a build passes or fails depends on the state of the branch build checks.
PRs are submitted (or retracted) by posting `bors merge` or `bors cahcel` in the comments just like for bors.
Testing is done by mocking the github API at a more abstract level.

A more practical implementation would require:
1. Listening to github webhooks, this means this should be a github app which reacts to events. Polling is too expensive. API calls are rate-limited. 
2. Notifying users that their builds are failing by posting a comment or something. Right now users are notified of successful builds by seeing their PRs getting merged, but there's nothing in place for failing builds.
3. Better test coverage, I only have the most minimal of tests.
4. A nice dashboard to visualize the state of the build system would be nice. (1) already involves running an HTTP server anyway.
5. Better pipelining heuristics. Right now `n` concurrent PRs implies `n(n+1)/2` concurrent CI runs, which is stupid. It's safe to assume that most builds will succeed, the heuristic should take that into account.
6. Better computer programming: better error handling, better API call concurrency, etc. 

There's bound to be more, this is just off the top of my head. Still, I think it's an idea worth exploring more.

