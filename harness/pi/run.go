package pi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const SupportedVersion = "0.99.2"
const systemPrompt = "Complete the requested task accurately. No tools are available. Return the requested answer."

// Config is host-owned, immutable execution configuration. Endpoint authorization,
// privacy and resource reservation must be enforced by Admit before Run starts.
// ExecutableSHA256 pins the installed Pi CLI artifact; Node and the installation
// are trusted host dependencies, not an OS sandbox.
type Prices struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}
type Progress struct{ Kind, Text string }
type Config struct {
	Prices                                                         *Prices
	OnProgress                                                     func(context.Context, Progress) error
	Executable, ExecutableSHA256, Provider, Model, BaseURL, APIKey string
	ContextTokens, MaxOutputTokens                                 int
	Timeout                                                        time.Duration
	Admit                                                          func(context.Context) (release func(), err error)
}

func (c Config) validate() error {
	if c.Prices == nil {
		return ErrProtocol
	}
	for _, price := range []float64{c.Prices.Input, c.Prices.Output, c.Prices.CacheRead, c.Prices.CacheWrite} {
		if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return ErrProtocol
		}
	}
	u, e := url.Parse(c.BaseURL)
	pin, e2 := hex.DecodeString(c.ExecutableSHA256)
	if !filepath.IsAbs(c.Executable) || e2 != nil || len(pin) != 32 || strings.ToLower(c.ExecutableSHA256) != c.ExecutableSHA256 || c.Provider == "" || c.Model == "" || len(c.Provider) > 256 || len(c.Model) > 256 || e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.APIKey == "" || c.ContextTokens < 8192 || c.ContextTokens > 1<<24 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 65536 || c.Timeout <= 0 || c.Timeout > 15*time.Minute || c.Admit == nil {
		return ErrProtocol
	}
	return nil
}

// Run launches one isolated, text-only, no-tool RPC session. Unsupported tool,
// retry, compaction or model-switch behavior fails closed. No retry is performed.
// Success means a settled valid execution, never a quality verdict.
func Run(ctx context.Context, c Config, prompt string) (result Result, runErr error) {
	if ctx == nil || ctx.Err() != nil || c.validate() != nil || strings.TrimSpace(prompt) == "" || !utf8.ValidString(prompt) || len(prompt) > MaxRecordBytes/2 || len(prompt)+len(systemPrompt)+4096+c.MaxOutputTokens > c.ContextTokens {
		return Result{}, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	release, e := c.Admit(ctx)
	if e != nil {
		return Result{}, e
	}
	if release == nil {
		return Result{}, ErrProtocol
	}
	defer release()
	file, e := os.Open(c.Executable)
	if e != nil {
		return Result{}, ErrRun
	}
	h := sha256.New()
	_, e = io.Copy(h, file)
	file.Close()
	if e != nil || hex.EncodeToString(h.Sum(nil)) != c.ExecutableSHA256 {
		return Result{}, ErrProtocol
	}
	dir, e := os.MkdirTemp("", "nexus-pi-")
	if e != nil {
		return Result{}, ErrRun
	}
	defer os.RemoveAll(dir)
	env := []string{"PATH=" + os.Getenv("PATH"), "PI_CODING_AGENT_DIR=" + dir, "PI_OFFLINE=1", "PI_TELEMETRY=0", "NO_COLOR=1"}
	version := exec.CommandContext(ctx, c.Executable, "--version")
	version.Env = env
	version.Dir = dir
	// Version stdout is capped independently of the RPC stream.
	versionOutput := &limitedBuffer{limit: 1024}
	version.Stdout = versionOutput
	version.Stderr = io.Discard
	if version.Run() != nil || strings.TrimSpace(string(versionOutput.data)) != SupportedVersion {
		return Result{}, ErrProtocol
	}
	models := map[string]any{"providers": map[string]any{c.Provider: map[string]any{"baseUrl": c.BaseURL, "api": "openai-completions", "models": []any{map[string]any{"id": c.Model, "name": c.Model, "reasoning": false, "input": []string{"text"}, "contextWindow": c.ContextTokens, "maxTokens": c.MaxOutputTokens, "cost": *c.Prices}}}}}
	auth := map[string]any{c.Provider: map[string]string{"type": "api_key", "key": c.APIKey}}
	settings := map[string]any{"compaction": map[string]bool{"enabled": false}, "retry": map[string]any{"enabled": false, "provider": map[string]int{"maxRetries": 0}}, "defaultTools": []string{}, "quietStartup": true}
	for name, value := range map[string]any{"models.json": models, "auth.json": auth, "settings.json": settings} {
		body, e := json.Marshal(value)
		if e != nil || os.WriteFile(filepath.Join(dir, name), body, 0600) != nil {
			return Result{}, ErrRun
		}
	}
	command := exec.CommandContext(ctx, c.Executable, "--mode", "rpc", "--no-session", "--offline", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--thinking", "off", "--system-prompt", systemPrompt, "--provider", c.Provider, "--model", c.Model)
	command.Env = env
	command.Dir = dir
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	input, e := command.StdinPipe()
	if e != nil {
		return Result{}, ErrRun
	}
	output, e := command.StdoutPipe()
	if e != nil {
		input.Close()
		return Result{}, ErrRun
	}
	if command.Start() != nil {
		input.Close()
		output.Close()
		return Result{}, ErrRun
	}
	defer func() {
		input.Close()
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		var exitErr error
		select {
		case exitErr = <-done:
		case <-time.After(2 * time.Second):
			command.Process.Kill()
			exitErr = <-done
		}
		output.Close()
		if exitErr != nil && runErr == nil {
			result = Result{}
			runErr = ErrRun
		}
	}()
	encoder := json.NewEncoder(input)
	if encoder.Encode(map[string]string{"id": "state", "type": "get_state"}) != nil {
		return Result{}, ErrRun
	}
	protocol, e := NewProtocol(c.Provider, c.Model)
	if e != nil {
		return Result{}, e
	}
	protocol.expectURL = c.BaseURL
	protocol.expectContext = c.ContextTokens
	protocol.expectOutput = c.MaxOutputTokens
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), MaxRecordBytes+1)
	sent := false
	wireBytes := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		wireBytes += len(line)
		if wireBytes > 16<<20 {
			return Result{}, ErrProtocol
		}
		settled, e := protocol.Consume(line)
		if e != nil {
			return Result{}, e
		}
		if protocol.Ready() && !sent {
			sent = true
			if encoder.Encode(map[string]string{"id": "prompt", "type": "prompt", "message": prompt}) != nil {
				return Result{}, ErrRun
			}
		}
		if c.OnProgress != nil {
			var event struct {
				Type                  string
				AssistantMessageEvent struct{ Type, Delta string }
			}
			json.Unmarshal(line, &event)
			progress := Progress{Kind: event.Type}
			if event.Type == "message_update" && event.AssistantMessageEvent.Type == "text_delta" {
				progress.Text = event.AssistantMessageEvent.Delta
			}
			if err := c.OnProgress(ctx, progress); err != nil {
				return Result{}, err
			}
		}
		if settled {
			return protocol.Result()
		}
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	return Result{}, ErrRun
}

type limitedBuffer struct {
	data  []byte
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-len(b.data) {
		return 0, ErrProtocol
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
