package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/ArronJablonowski/NexusRouter/connectors/comfyui"
)

const comfyUsage = `nexus comfy info|nodes|queue|submit|status|wait|download|cancel-pending
  --endpoint URL     Explicit ComfyUI origin (use a private SSH tunnel for Spark)
  --workflow FILE    API-format workflow JSON (submit)
  --receipt FILE     Private write-ahead record; submit requires a new file
  --prompt-id ID     Original prompt ID, or use --receipt (status/wait/download/cancel-pending)
  --timeout 5m       Overall operation deadline; a timeout never cancels/resubmits
  --output FILE      New local destination (download)
  --node ID          Output node for download; --image selects its image (default 0)
No global interrupt, queue clear, model install, or service control is exposed.
`

func runComfy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprint(stdout, comfyUsage)
		return 0
	}
	action := args[0]
	switch action {
	case "info", "nodes", "queue", "submit", "status", "wait", "download", "cancel-pending":
	default:
		fmt.Fprint(stderr, comfyUsage)
		return 2
	}
	fs := flag.NewFlagSet("comfy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "", "ComfyUI origin")
	workflow := fs.String("workflow", "", "API workflow")
	receipt := fs.String("receipt", "", "submission receipt")
	id := fs.String("prompt-id", "", "original prompt ID")
	timeout := fs.Duration("timeout", 5*time.Minute, "deadline")
	output := fs.String("output", "", "new destination")
	node := fs.String("node", "", "output node")
	index := fs.Int("image", 0, "image index")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *timeout <= 0 {
		return 2
	}
	fail := func(e error) int { fmt.Fprintln(stderr, e); return 1 }
	if *receipt != "" && action != "submit" {
		r, e := comfyui.LoadReceipt(*receipt)
		if e != nil {
			return fail(e)
		}
		if *endpoint != "" && *endpoint != r.Endpoint {
			return fail(errors.New("endpoint does not match receipt"))
		}
		*endpoint = r.Endpoint
		if *id != "" && *id != r.PromptID {
			return fail(errors.New("prompt ID does not match receipt"))
		}
		*id = r.PromptID
		if *id == "" {
			return fail(errors.New("receipt has no confirmed prompt ID; inspect queue/history manually using client_id; do not resubmit"))
		}
	}
	c, e := comfyui.New(*endpoint)
	if e != nil {
		return fail(e)
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	emit := func(v any) int {
		if e := json.NewEncoder(stdout).Encode(v); e != nil {
			return fail(e)
		}
		return 0
	}
	switch action {
	case "info", "nodes", "queue":
		b, e := c.Read(ctx, action)
		if e != nil {
			return fail(e)
		}
		return emit(b)
	case "submit":
		if *workflow == "" || *receipt == "" {
			fmt.Fprintln(stderr, "submit requires --workflow and a new --receipt")
			return 2
		}
		f, e := os.Open(*workflow)
		if e != nil {
			return fail(e)
		}
		b, e := io.ReadAll(io.LimitReader(f, comfyui.MaxJSON+1))
		f.Close()
		if e != nil {
			return fail(e)
		}
		w, e := comfyui.ParseWorkflow(b)
		if e != nil {
			return fail(e)
		}
		r, e := c.SubmitOnce(ctx, w, *receipt)
		if e != nil {
			return fail(e)
		}
		return emit(r)
	case "status":
		h, e := c.History(ctx, *id)
		if e != nil {
			return fail(e)
		}
		if h == nil {
			return emit(map[string]string{"state": "not_in_history", "prompt_id": *id})
		}
		return emit(h)
	case "wait":
		h, e := c.Wait(ctx, *id, time.Second)
		if e != nil {
			return fail(e)
		}
		return emit(h)
	case "cancel-pending":
		if e := c.DeletePending(ctx, *id); e != nil {
			return fail(e)
		}
		return emit(map[string]string{"state": "pending_delete_requested", "prompt_id": *id})
	case "download":
		if *output == "" || *node == "" || *index < 0 {
			fmt.Fprintln(stderr, "download requires --output, --node and a nonnegative --image")
			return 2
		}
		h, e := c.History(ctx, *id)
		if e != nil {
			return fail(e)
		}
		if h == nil || !h.Status.Completed || h.Status.StatusStr != "success" {
			return fail(errors.New("prompt has no successful completed output"))
		}
		images := h.Outputs[*node].Images
		if *index >= len(images) {
			return fail(errors.New("output image not found"))
		}
		b, e := c.Image(ctx, images[*index])
		if e != nil {
			return fail(e)
		}
		// Never follow or overwrite a pre-existing destination.
		f, e := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return fail(e)
		}
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			os.Remove(*output)
			return fail(e)
		}
		if ce != nil {
			os.Remove(*output)
			return fail(ce)
		}
		return emit(map[string]string{"output": *output})
	}
	return 2
}
