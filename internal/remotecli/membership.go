package remotecli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func readTrustInput(input io.Reader, value any) error {
	if input == nil {
		return remote.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(input, remote.MaxBody+1))
	if err != nil || len(body) > remote.MaxBody {
		return remote.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return remote.ErrInvalid
	}
	return nil
}

func membershipOperation(registry remote.TrustFile, operation, instance, expected string, input io.Reader, output io.Writer) error {
	var next remote.Registry
	var err error
	switch operation {
	case "peers":
		if instance != "" || expected != "" {
			return remote.ErrInvalid
		}
		next, err = registry.Read()
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(struct {
			Registry remote.Registry `json:"registry"`
			Digest   string          `json:"digest"`
		}{next, next.Digest()})
	case "pair":
		if instance != "" || expected == "" {
			return remote.ErrInvalid
		}
		var peer remote.Peer
		if err = readTrustInput(input, &peer); err != nil {
			return err
		}
		next, err = registry.Pair(peer, expected)
	case "revoke":
		next, err = registry.Revoke(instance, expected)
	default:
		return remote.ErrInvalid
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, next.Digest())
	return err
}
