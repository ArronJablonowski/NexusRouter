# Authorized immutable publication execution

`githubpublish.PublishRelease` is NexusRouter's mockable publication state
machine. It accepts an already-authorized exact plan and does not obtain or
create credentials. Its transport and GitHub origins are injected; repository
tests use loopback HTTP only.

Before its first mutation it checks GitHub's repository immutable-release
endpoint and proves both the exact tag and release name are absent. It then
creates an exclusive durable operation journal and records intent/result pairs
around each mutation:

1. Create the exact annotated tag object and durably record its returned SHA.
2. Create `refs/tags/<tag>` pointing to that tag-object SHA.
3. Create the exact draft release.
4. Upload each exact authorized asset.
5. Read back the draft, complete asset set, tag ref, and peeled tag object.
6. Recheck the immutable-release setting.
7. Perform one exact `draft: true` to `draft: false` transition carrying the
   authorized prerelease and `make_latest` decisions.
8. Require the published release to report `immutable: true`, then reread the
   exact assets and peeled annotated tag and independently confirm the approved
   `make_latest` outcome through the latest-release endpoint before recording
   confirmation.

An attempted mutation is non-retryable unless separate read-only reconciliation
proves what happened. The journal recognizes an exact immutable, non-draft
remote release after a lost publish response only when the complete ordered
tag-object, tag-reference, draft, asset, and publish history is durable and the
annotated-tag and latest-release observations match. GitHub's immutable setting is
rechecked immediately before publication, but a remote administrator could
still race the check; the required post-transition `immutable: true` response
and independent verifier therefore remain mandatory.

The release API's `target_commitish` response is not used as commit authority
once the tag exists. Commit identity comes from the independently reread tag
reference and annotated tag object's direct commit target.
