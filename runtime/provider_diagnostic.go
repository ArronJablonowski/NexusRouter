package runtime

import (
	"errors"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func providerStreamDetail(err error) string {
	var failure *providers.Failure
	if errors.As(err, &failure) && failure != nil {
		return failure.SafeStreamDetail()
	}
	return ""
}

// Diagnostics are metadata, not model output. Keeping Text empty preserves the
// strict first-turn recovery and usage-attribution boundary.
func providerFailureData(kind Kind, code string, cause error) Data {
	data := Data{Code: code}
	if kind == TaskFailed {
		data.ProviderStreamDetail = providerStreamDetail(cause)
	}
	return data
}
