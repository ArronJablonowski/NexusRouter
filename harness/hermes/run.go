package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

var ErrRun = errors.New("Hermes execution or cleanup failed")

const systemPrompt = "Complete the requested task accurately. No tools are available. Return the requested answer."

type Prices struct{ Input, Output, CacheRead, CacheWrite float64 }

// Config is immutable host authority. Admit must enforce privacy, endpoint and
// resource policies. Transport must be the host policy transport. The executable
// pin covers the Python executable. The pinned source checkout and installed
// Python dependencies remain trusted host code, not an OS sandbox.
type Config struct {
	// RuntimeSHA256 is the host-attested installed dependency manifest digest.
	RuntimeSHA256 string
	// Executable is the installed dependency-environment Python interpreter.
	SourceDir                                                    string
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
	if !processSupported() || !filepath.IsAbs(c.Executable) || !filepath.IsAbs(c.SourceDir) || !digest(c.ExecutableSHA256) || !digest(c.RuntimeSHA256) || !digest(c.TransportPolicySHA256) || !label(c.Provider) || strings.Contains(c.Provider, "/") || !label(c.Model) || c.ModelRevision == "" || c.Prices == nil || c.Transport == nil || c.Admit == nil || c.ContextTokens < 8192 || c.ContextTokens > 1<<24 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 65536 || c.MaxOutputTokens >= c.ContextTokens || c.Timeout <= 0 || c.Timeout > 15*time.Minute || e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (c.UpstreamProtocol != "" && c.UpstreamProtocol != "openai_compatible" && c.UpstreamProtocol != "ollama") {
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
func Run(ctx context.Context, c Config, prompt string) (Result, error) {
	return runConfigured(ctx, c, prompt, nil)
}

func runConfigured(ctx context.Context, c Config, prompt string, agent *agentExecution) (result Result, runErr error) {
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
	if agent != nil {
		identity, err = agent.identity, nil
	}
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
	dir, err := os.MkdirTemp("", "nexus-hermes-")
	if err != nil {
		return Result{}, ErrRun
	}
	defer func() {
		if e := os.RemoveAll(dir); e != nil && runErr == nil {
			result = Result{}
			runErr = ErrRun
		}
	}()
	var base, key string
	var closeGateway func()
	var gateway *textgateway.AgentGateway
	gc := textgateway.Config{DefaultMissingOutputLimit: true, UpstreamProtocol: c.UpstreamProtocol, BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Transport: c.Transport, Messages: c.Messages}
	if agent == nil {
		base, key, verified, closeGateway, err = textgateway.Start(ctx, gc)
	} else {
		gateway, err = textgateway.StartAgent(ctx, textgateway.AgentConfig{Config: gc, Actual: identity, Session: agent.session, Tools: agent.config.Tools, NamespaceTools: true})
		if err == nil {
			base, key, closeGateway = gateway.BaseURL, gateway.Token, gateway.Close
		}
	}
	if err != nil {
		return Result{}, err
	}
	defer closeGateway()
	configPath, env, err := isolatedFiles(dir, base, key, c.Model)
	if err != nil {
		return Result{}, err
	}
	if agent != nil {
		env = append(env, "HOME="+dir)
		raw, e := os.ReadFile(configPath)
		if e != nil {
			return Result{}, ErrRun
		}
		var private map[string]any
		if json.Unmarshal(raw, &private) != nil {
			return Result{}, ErrProjection
		}
		private["agent"] = map[string]any{"max_turns": agent.config.MaxTurns}
		private["tools"] = map[string]any{"tool_search": map[string]string{"enabled": "off"}}
		raw, e = json.Marshal(private)
		if e != nil || os.WriteFile(configPath, raw, 0600) != nil {
			return Result{}, ErrRun
		}
	}
	if err := verifySource(ctx, c, env, dir); err != nil {
		return Result{}, err
	}
	bootstrap := "import sys; sys.path.insert(0," + strconv.Quote(c.SourceDir) + "); import hermes_bootstrap; from hermes_cli.main import main; sys.exit(main())"
	command := exec.CommandContext(ctx, c.Executable, "-I", "-c", bootstrap, "chat", "--model", c.Model, "--provider", "nexus-gateway", "--reasoning", "none", "--toolsets", "all", "--max-turns", "1", "--run-budget", strconv.Itoa(int(math.Ceil(c.Timeout.Seconds()))), "--ignore-rules", "--query-file", "-", "--oneshot", "--format", "stream-json")
	input := prompt
	if agent != nil {
		bridge := filepath.Join(dir, "bridge.py")
		if os.WriteFile(bridge, []byte(agentBridgeSource), 0600) != nil {
			return Result{}, ErrRun
		}
		entries, _ := agentToolEntries(agent.config.Tools)
		settings := map[string]any{"source": c.SourceDir, "base_url": base, "api_key": key, "tool_token": gateway.ToolToken, "model": c.Model, "prompt": prompt, "max_output_tokens": c.MaxOutputTokens, "max_turns": agent.config.MaxTurns, "timeout_seconds": int(math.Ceil(c.Timeout.Seconds())), "tools": entries}
		raw, e := json.Marshal(settings)
		if e != nil {
			return Result{}, ErrProjection
		}
		input = string(raw)
		command = exec.CommandContext(ctx, c.Executable, "-I", bridge)
	}
	command.Env = env
	command.Dir = dir
	command.Stdin = strings.NewReader(input)
	command.Stderr = io.Discard
	output := &boundedOutput{limit: MaxStreamBytes, cancel: cancel}
	command.Stdout = output
	err = runProcess(command)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		return Result{}, ErrRun
	}
	if agent != nil {
		transcript, e := gateway.Transcript()
		if e != nil {
			return Result{}, e
		}
		projection, e := parseAgentProjection(output.data, command.ProcessState.ExitCode(), c.Model, transcript)
		if e != nil {
			return Result{}, e
		}
		final, e := gateway.Final()
		if e != nil || final != projection.Text {
			return Result{}, ErrProjection
		}
		return Result{Text: final, Identity: identity}, nil
	}
	projection, err := ParseProjection(output.data, command.ProcessState.ExitCode(), c.Model)
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
	if runtime.GOOS == "linux" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	err := processaudit.Run(cmd)
	if cmd.Process != nil && groupStillAlive(cmd) {
		_ = killProcessGroup(cmd)
		return ErrRun
	}
	return err
}
