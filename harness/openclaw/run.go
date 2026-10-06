package openclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

var ErrRun = errors.New("OpenClaw execution or cleanup failed")

const systemPrompt = "Complete the requested task accurately. No tools are available. Return the requested answer."

type Prices struct{ Input, Output, CacheRead, CacheWrite float64 }

// Config is immutable host authority. Admit must enforce privacy, endpoint and
// resource policies. Transport must be the host policy transport. The executable
// pin covers the launcher; Node and its installed modules remain trusted host
// dependencies, not an OS sandbox or an attestation of the whole installation.
type Config struct {
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
	if !processSupported() || !filepath.IsAbs(c.Executable) || !digest(c.ExecutableSHA256) || !digest(c.TransportPolicySHA256) || !identifier(c.Provider) || strings.Contains(c.Provider, "/") || !identifier(c.Model) || c.ModelRevision == "" || c.Prices == nil || c.Transport == nil || c.Admit == nil || c.ContextTokens < 8192 || c.ContextTokens > 1<<24 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 65536 || c.MaxOutputTokens >= c.ContextTokens || c.Timeout <= 0 || c.Timeout > 15*time.Minute || e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (c.UpstreamProtocol != "" && c.UpstreamProtocol != "openai_compatible" && c.UpstreamProtocol != "ollama") {
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
	contextBody, err := contextMessages(c.Messages)
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
	dir, err := os.MkdirTemp("", "nexus-openclaw-")
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
	gc := textgateway.Config{UpstreamProtocol: c.UpstreamProtocol, BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Transport: c.Transport, Messages: c.Messages}
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
	path, env, err := isolatedFiles(dir, base, key, c.Provider, c.Model, c.ContextTokens, c.MaxOutputTokens)
	if err != nil {
		return Result{}, err
	}
	var receipts string
	if agent != nil {
		env = append(env, "HOME="+dir)
		receipts, err = agentFiles(dir, path, agent, gateway, c.Timeout)
		if err != nil {
			return Result{}, err
		}
	}
	versionCtx, versionCancel := context.WithTimeout(ctx, 10*time.Second)
	version := exec.CommandContext(versionCtx, c.Executable, "--version")
	version.Env = env
	version.Dir = dir
	versionOutput := &boundedOutput{limit: 1024, cancel: versionCancel}
	version.Stdout = versionOutput
	version.Stderr = io.Discard
	versionErr := runProcess(version)
	versionCancel()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if versionErr != nil || !regexp.MustCompile(`^OpenClaw `+regexp.QuoteMeta(SupportedVersion)+` \([a-f0-9]+\)$`).MatchString(strings.TrimSpace(string(versionOutput.data))) {
		return Result{}, ErrProjection
	}
	command := exec.CommandContext(ctx, c.Executable, "agent", "exec", "--config", path, "--cwd", dir, "--model", c.Provider+"/"+c.Model, "--thinking", "off", "--code-mode", "direct", "--message-file", "-", "--timeout", strconv.Itoa(int(math.Ceil(c.Timeout.Seconds()))), "--json")
	command.Env = env
	command.Dir = dir
	command.Stdin = strings.NewReader(prompt)
	command.Stderr = io.Discard
	output := &boundedOutput{limit: MaxEnvelopeBytes, cancel: cancel}
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
		projection, e := parseAgentProjection(output.data, command.ProcessState.ExitCode(), c.Provider, c.Model, receipts, transcript)
		if e != nil {
			return Result{}, e
		}
		final, e := gateway.Final()
		if e != nil || projection.Text != strings.TrimRightFunc(final, jsWhitespace) {
			return Result{}, ErrProjection
		}
		return Result{Text: projection.Text, Identity: identity}, nil
	}
	projection, err := ParseProjection(output.data, command.ProcessState.ExitCode(), c.Provider, c.Model)
	if err != nil {
		return Result{}, err
	}
	completion, err := verified()
	if err != nil || projection.Text != strings.TrimRightFunc(completion.Text, jsWhitespace) {
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
	err := processaudit.Run(cmd)
	if cmd.Process != nil && groupStillAlive(cmd) {
		_ = killProcessGroup(cmd)
		return ErrRun
	}
	return err
}
