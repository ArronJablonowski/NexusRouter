package resources

import (
	"context"
	"strings"
)

// DGX Spark's GB10 shares system RAM with the CPU. Identify the observed
// platform through kernel DMI, not missing nvidia-smi VRAM (which can also mean
// an unavailable discrete GPU). Unknown platforms retain the existing behavior.
// Capacity remains the proc/cgroup RAM measurement; no second pool is invented.
func linuxUnifiedMemory(ctx context.Context, read func(context.Context, string) ([]byte, error)) bool {
	if ctx == nil || read == nil || ctx.Err() != nil {
		return false
	}
	identity := func(path, want string) bool {
		body, err := read(ctx, path)
		return err == nil && len(body) <= 256 && ctx.Err() == nil && strings.TrimSpace(string(body)) == want
	}
	return identity("/sys/class/dmi/id/sys_vendor", "NVIDIA") &&
		identity("/sys/class/dmi/id/product_name", "NVIDIA_DGX_Spark")
}
