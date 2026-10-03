package remotecli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"io"
	"time"
)

func loggingOperation(ctx context.Context, op string, args []string, out, errOut io.Writer) error {
	f := flag.NewFlagSet(op, flag.ContinueOnError)
	f.SetOutput(errOut)
	store := f.String("log-store", "", "absolute private commander log directory")
	trust := f.String("trust", "", "paired peer registry; only peers with explicit logs permission are collected")
	cert := f.String("cert", "", "local PEM certificate")
	key := f.String("key", "", "private PEM key")
	ca := f.String("ca", "", "CA PEM")
	source := f.String("instance", "", "source instance; omit to collect all logs-enabled peers")
	stream := f.String("stream", "runtime", "runtime or security (logs-read)")
	task := f.String("task", "", "optional task filter (logs-read)")
	after := f.Int64("after", 0, "last source position (logs-read)")
	limit := f.Int("limit", 100, "maximum records (logs-read)")
	watch := f.Bool("watch", false, "continuously collect; failures remain visible and retry on next interval")
	interval := f.Duration("interval", 5*time.Second, "collection interval, 5s to 1h")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *interval < 5*time.Second || *interval > time.Hour {
		return remote.ErrInvalid
	}
	db, err := remote.OpenLogStore(*store)
	if err != nil {
		return err
	}
	defer db.Close()
	encode := json.NewEncoder(out)
	if op == "logs-status" {
		status, e := db.Status(ctx)
		if e != nil {
			return e
		}
		return encode.Encode(status)
	}
	if op == "logs-read" {
		records, e := db.Read(ctx, *source, *stream, *task, *after, *limit)
		if e != nil {
			return e
		}
		return encode.Encode(records)
	}
	c := remote.Client{Trust: remote.TrustFile(*trust), Credentials: remote.Credentials{CertificateFile: *cert, KeyFile: *key, CAFile: *ca}}
	for {
		reg, e := c.Trust.Read()
		if e != nil {
			return e
		}
		found := false
		var cycleErr error
		for _, peer := range reg.Peers {
			if *source != "" && peer.ID != *source {
				continue
			}
			allowed := false
			for _, op := range peer.Operations {
				if op == "logs" {
					allowed = true
				}
			}
			if !allowed {
				continue
			}
			found = true
			for _, stream := range []string{"runtime", "security"} {
				if e = c.CollectLogs(ctx, db, peer.ID, stream); e != nil {
					cycleErr = errors.Join(cycleErr, e)
				}
			}
		}
		if !found {
			return remote.ErrDenied
		}
		status, e := db.Status(ctx)
		if e != nil {
			return e
		}
		if e = encode.Encode(status); e != nil {
			return e
		}
		if !*watch {
			return cycleErr
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
