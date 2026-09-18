package app

import "github.com/ArronJablonowski/DarwinRouter/internal/config"

// delegatedReadsConfigured distinguishes an operator-approved local worker
// capability from direct filesystem authority. The cloud coordinator may see
// delegate, but never read_file, only when this deliberately narrow shape is
// active. Any additional direct tool surface keeps the existing local-only
// admission rule.
func delegatedReadsConfigured(settings config.Settings) bool {
	return settings.Tools.Enabled && settings.Workers.DelegateReadTools && settings.Workers.DelegateModel != "" &&
		!settings.Tools.CreateEnabled && !settings.Tools.ReplaceEnabled &&
		!settings.Tools.WorkboardReadEnabled && !settings.Tools.WorkboardWriteEnabled
}

func cloudDelegatedReads(settings config.Settings, request Request, model config.Model) bool {
	return request.delegatedParent == "" && model.Locality == "cloud" && len(request.toolExtension.Names()) == 0 &&
		delegatedReadsConfigured(settings)
}
