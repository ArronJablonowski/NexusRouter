package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ArronJablonowski/NexusRouter/scripts"
)

var ErrScheduleChanged = errors.New("cleanup schedule changed")

type ScheduleSettings struct {
	Enabled bool `json:"enabled"`
	Hour    int  `json:"hour"`
	Minute  int  `json:"minute"`
	Weekday *int `json:"weekday"`
}

func (s ScheduleSettings) Valid() bool {
	return s.Hour >= 0 && s.Hour <= 23 && s.Minute >= 0 && s.Minute <= 59 && (s.Weekday == nil || (*s.Weekday >= 0 && *s.Weekday <= 6))
}

type Schedule struct {
	ScheduleSettings
	Loaded   bool   `json:"loaded"`
	Running  bool   `json:"running"`
	State    string `json:"state"`
	Timezone string `json:"timezone"`
	Digest   string `json:"digest"`
}
type ScheduleReport struct {
	Version   int      `json:"version"`
	Schedule  Schedule `json:"schedule"`
	Recovered bool     `json:"recovered"`
}
type ScheduleUpdate struct {
	Version        int              `json:"version"`
	ExpectedDigest string           `json:"expected_digest"`
	Settings       ScheduleSettings `json:"settings"`
}

func (s Service) Schedule(ctx context.Context, input *ScheduleUpdate) (ScheduleReport, error) {
	var out ScheduleReport
	if runtime.GOOS != "darwin" {
		return out, errors.New("cleanup scheduling unavailable")
	}
	if input != nil && (input.Version != 1 || !tokenPattern.MatchString(input.ExpectedDigest) || !input.Settings.Valid()) {
		return out, errors.New("invalid cleanup schedule")
	}
	for name, expected := range map[string][]byte{"cleanup-nexus-home.py": scripts.CleanupSource, "cleanup-schedule.py": scripts.CleanupScheduleSource} {
		p := filepath.Join(s.Home, "tools/maintenance", name)
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 1<<20 {
			return out, errors.New("schedule utility not installed")
		}
		source, err := os.ReadFile(p)
		if err != nil || sha256.Sum256(source) != sha256.Sum256(expected) {
			return out, errors.New("schedule utility needs qualification")
		}
	}
	args := []string{"-B", filepath.Join(s.Home, "tools/maintenance/cleanup-schedule.py"), "--home", s.Home}
	var body []byte
	if input != nil {
		args = append(args, "--apply")
		body, _ = json.Marshal(struct {
			Settings ScheduleSettings `json:"settings"`
			Expected string           `json:"expected_digest"`
		}{input.Settings, input.ExpectedDigest})
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", args...)
	configureScheduleCommand(cmd)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + filepath.Dir(s.Home), "LANG=en_US.UTF-8"}
	buf := &limitedBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = io.Discard
	err := cmd.Run()
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(buf.Bytes(), &failure)
	if failure.Error == "schedule_changed" {
		return out, ErrScheduleChanged
	}
	if err != nil || json.Unmarshal(buf.Bytes(), &out) != nil || out.Version != 1 || !out.Schedule.Valid() || !tokenPattern.MatchString(out.Schedule.Digest) {
		return ScheduleReport{}, errors.New("schedule unavailable; refresh status before retrying")
	}
	return out, nil
}

// Require explicit enabled/time fields so an incomplete request cannot disable a job.
func (u *ScheduleUpdate) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Version  int    `json:"version"`
		Expected string `json:"expected_digest"`
		Settings struct {
			Enabled *bool `json:"enabled"`
			Hour    *int  `json:"hour"`
			Minute  *int  `json:"minute"`
			Weekday *int  `json:"weekday"`
		} `json:"settings"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.Settings.Enabled == nil || wire.Settings.Hour == nil || wire.Settings.Minute == nil {
		return errors.New("schedule fields required")
	}
	*u = ScheduleUpdate{Version: wire.Version, ExpectedDigest: wire.Expected, Settings: ScheduleSettings{Enabled: *wire.Settings.Enabled, Hour: *wire.Settings.Hour, Minute: *wire.Settings.Minute, Weekday: wire.Settings.Weekday}}
	if !u.Valid() {
		return errors.New("invalid schedule")
	}
	return nil
}
func (u ScheduleUpdate) Valid() bool {
	return u.Version == 1 && tokenPattern.MatchString(u.ExpectedDigest) && u.Settings.Valid()
}
