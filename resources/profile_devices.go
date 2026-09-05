package resources

import "context"

// ProfileWithGPUs adds independent device observations to the host profile.
// It does not merge device capacities or prove backend placement. Callers opt
// in only when they have explicit model/device bindings to evaluate.
func ProfileWithGPUs(ctx context.Context) (Snapshot, error) {
	return profileWithGPUs(ctx, Profile, SurveyGPUs)
}

func profileWithGPUs(ctx context.Context, host func(context.Context) (Snapshot, error), survey func(context.Context) (GPUInventory, error)) (Snapshot, error) {
	snapshot, err := host(ctx)
	if err != nil || ctx.Err() != nil {
		return Snapshot{}, ErrProfile
	}
	inventory, err := survey(ctx)
	if err != nil || ctx.Err() != nil {
		return Snapshot{}, ErrProfile
	}
	snapshot.GPUs = &inventory
	return snapshot, nil
}
