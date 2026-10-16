// Package scripts exposes the reviewed maintenance source for integrity checking.
package scripts

import _ "embed"

// CleanupSource is the qualified standalone maintenance program.
//
//go:embed cleanup-nexus-home.py
var CleanupSource []byte

// CleanupScheduleSource is the reviewed local LaunchAgent control helper.
//
//go:embed cleanup-schedule.py
var CleanupScheduleSource []byte
