# Hybrid remote delegation

Remote requests default to direct model execution, with no implicit worker delegation. An explicit `execution` object selects `commander` mode. The model must equal the destination's configured `webui.default_model`; inferred display labels do not authorize orchestration.

Commander requests supply `specialist_ids` (1–8 unique IDs), `max_calls` (1–16, additionally capped by destination policy), `deadline` (absolute UTC, within one hour), and `depth: 1`. Every specialist must be permitted by the authenticated caller's existing model allowlist. The current bounded implementation requires private local inference and zero estimated model cost; cloud orchestration is rejected rather than presenting estimated cost as a hard spending guarantee.

The Commander can use `delegate` and `delegate_batch` with an explicit `model_id` to split the assignment and synthesize results. Workers cannot recursively delegate, access ambient memory/skills, or write files. Call counts are shared across sequential and parallel delegation. Existing RAM reservations and destination admission apply independently to each execution; capacity exhaustion returns a worker failure rather than waiting while the Commander holds resources. No fallback expands the model allowlist.

The execution policy is persisted in the submission and rechecked at execution. The absolute deadline includes queue time. Child calls also retain the existing 30-second worker timeout. Parent cancellation propagates to children through the existing worker runtime. The controlling host retains its durable request/submission IDs, retrieves status and result events through existing remote APIs, and remains responsible for the overall assignment. Remote results are untrusted task output, not authority to initiate another remote hop. Older hosts reject the unknown execution contract; callers must not silently fall back or redispatch under a new ID.

The Connected Systems dispatch form exposes both modes and previews exact bounds before sending. Direct remains the default. This implementation has not been tested or deployed: validation and scheduled work remain stopped at the user's request.
