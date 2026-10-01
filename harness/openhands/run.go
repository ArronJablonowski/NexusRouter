package openhands

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

var ErrRun = errors.New("OpenHands execution or cleanup failed")

//go:embed bridge.py
var bridgeSource string

const MaxRecordBytes = 1 << 20

const systemPrompt = "Complete the requested task accurately. No tools are available. Return the requested answer."

type Prices struct{ Input, Output, CacheRead, CacheWrite float64 }

// Config is immutable host authority. Admit must enforce privacy, endpoint and
// resource policies. Transport must be the host policy transport. The executable
// pin covers the Python interpreter. The runtime digest attests dependencies.
// Installed SDK packages and host libraries remain trusted code;
// configuration isolation is not an OS sandbox.
type Config struct {
	RuntimeSHA256                                                string // Host-attested dependency manifest digest, not a package-file integrity proof.
	Executable, ExecutableSHA256, Provider, Model, ModelRevision string
	BaseURL, APIKey, UpstreamProtocol, TransportPolicySHA256     string
	ContextTokens, MaxOutputTokens                               int
	Timeout                                                      time.Duration
	Prices                                                       *Prices
	Messages                                                     []providers.Message
	Transport                                                    http.RoundTripper
	Admit                                                        func(context.Context) (func(), error)
}

// Result is a completed execution, never an automatic quality acceptance.
// Only upstream gateway usage is exported; harness-normalized counts are ignored.
type Result struct {
	// Usage is measured at the upstream gateway; nil means unreported.
	Usage    *providers.Usage
	Text     string
	Identity harness.Identity
}

func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}

func (c Config) validate() error {
	u, e := url.Parse(c.BaseURL)
	if !processSupported() || !filepath.IsAbs(c.Executable) || !digest(c.ExecutableSHA256) || !digest(c.RuntimeSHA256) || !digest(c.TransportPolicySHA256) || !label(c.Provider) || strings.Contains(c.Provider, "/") || !label(c.Model) || c.ModelRevision == "" || c.Prices == nil || c.Transport == nil || c.Admit == nil || c.ContextTokens < 8192 || c.ContextTokens > 1<<24 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 65536 || c.MaxOutputTokens >= c.ContextTokens || c.Timeout <= 0 || c.Timeout > 15*time.Minute || e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (c.UpstreamProtocol != "" && c.UpstreamProtocol != "openai_compatible" && c.UpstreamProtocol != "ollama") {
		return ErrProjection
	}
	switch reflect.ValueOf(c.Transport).Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
		if reflect.ValueOf(c.Transport).IsNil() {
			return ErrProjection
		}
	}
	for _, p := range []float64{c.Prices.Input, c.Prices.Output, c.Prices.CacheRead, c.Prices.CacheWrite} {
		if p < 0 || math.IsNaN(p) || math.IsInf(p, 0) {
			return ErrProjection
		}
	}
	return nil
}

// Run performs one admitted isolated text-only execution. The native process
// must exit and its projection must bind to the gateway's verified response.
// Incomplete, canceled or uncertain runs return no accepted output and no retry.
func Run(ctx context.Context, c Config, prompt string) (result Result, runErr error) {
	if ctx == nil || ctx.Err() != nil || c.validate() != nil || !utf8.ValidString(prompt) || strings.TrimSpace(prompt) == "" || len(prompt) > MaxRecordBytes/2 {
		return Result{}, ErrProjection
	}
	prices := *c.Prices
	c.Prices = &prices
	c.Messages = append([]providers.Message(nil), c.Messages...)
	if len(c.Messages) == 0 {
		c.Messages = []providers.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: prompt}}
	}
	contextBody, err := textgateway.ContextMessages(c.Messages)
	if err != nil || len(contextBody)+4096+c.MaxOutputTokens > c.ContextTokens || len(prompt)+4096+c.MaxOutputTokens > c.ContextTokens {
		return Result{}, ErrProjection
	}
	identity, err := c.Identity()
	if err != nil {
		return Result{}, err
	}
	// Run this after process, gateway and private-state cleanup so late failures
	// retain verified consumption without releasing accepted text.
	var verified func() (textgateway.Completion, error)
	defer func() {
		if runErr != nil && verified != nil {
			completion, e := verified()
			if e == nil && completion.Usage != nil {
				result = Result{Identity: identity, Usage: completion.Usage}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	release, err := c.Admit(ctx)
	if err != nil {
		return Result{}, err
	}
	if release == nil {
		return Result{}, ErrProjection
	}
	defer release()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	file, err := os.Open(c.Executable)
	if err != nil {
		return Result{}, ErrRun
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != c.ExecutableSHA256 {
		return Result{}, ErrProjection
	}
	dir, err := os.MkdirTemp("", "nexus-openhands-")
	if err != nil {
		return Result{}, ErrRun
	}
	defer func() {
		if e := os.RemoveAll(dir); e != nil && runErr == nil {
			result = Result{}
			runErr = ErrRun
		}
	}()
	base, key, verified, closeGateway, err := textgateway.Start(ctx, textgateway.Config{DefaultMissingOutputLimit: true, UpstreamProtocol: c.UpstreamProtocol, BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Transport: c.Transport, Messages: c.Messages})
	if err != nil {
		return Result{}, err
	}
	defer closeGateway()
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "OPENHANDS_SUPPRESS_BANNER=1", "LITELLM_LOCAL_MODEL_COST_MAP=True"}
	bridge := filepath.Join(dir, "bridge.py")
	if err := os.WriteFile(bridge, []byte(bridgeSource), 0600); err != nil {
		return Result{}, ErrRun
	}
	config, err := json.Marshal(map[string]any{"base_url": base, "api_key": key, "model": "openai/" + c.Model, "workspace": dir, "max_output_tokens": c.MaxOutputTokens, "context_tokens": c.ContextTokens, "timeout_seconds": int(math.Ceil(c.Timeout.Seconds()))})
	if err != nil {
		return Result{}, ErrProjection
	}
	command := exec.CommandContext(ctx, c.Executable, "-I", bridge)
	command.Env = env
	command.Dir = dir
	command.Stdin = strings.NewReader(string(config))
	command.Stderr = io.Discard
	output := &boundedOutput{limit: MaxProjectionBytes, cancel: cancel}
	command.Stdout = output
	err = runProcess(command)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		return Result{}, ErrRun
	}
	projection, err := ParseProjection(output.data, command.ProcessState.ExitCode(), "openai/"+c.Model)
	if err != nil {
		return Result{}, err
	}
	completion, err := verified()
	if err != nil || projection.Text != completion.Text {
		return Result{}, ErrProjection
	}
	return Result{Text: projection.Text, Identity: identity, Usage: completion.Usage}, nil
}

type boundedOutput struct {
	data   []byte
	limit  int
	cancel context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-len(b.data) {
		if b.cancel != nil {
			b.cancel()
		}
		return 0, ErrProjection
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func runProcess(cmd *exec.Cmd) error {
	if !processSupported() {
		return ErrRun
	}
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if cmd.Process != nil && groupStillAlive(cmd) {
		_ = killProcessGroup(cmd)
		return ErrRun
	}
	return err
}

func label(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
