package resources

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"math"
	"strconv"
	"strings"
)

// NVIDIA documents selective queries and CSV/noheader/nounits at
// https://docs.nvidia.com/deploy/nvidia-smi/index.html . Memory values are MiB.
func probeNVIDIAGPUs(ctx context.Context) ([]GPUDevice, error) {
	body, err := runProbe(ctx, "/usr/bin/nvidia-smi", "--query-gpu=uuid,memory.total,memory.free", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, ErrProfile
	}
	return parseNVIDIAGPUs(body)
}

func parseNVIDIAGPUs(body []byte) ([]GPUDevice, error) {
	if len(body) == 0 || len(body) > 64<<10 {
		return nil, ErrProfile
	}
	for _, b := range body {
		if b > 127 || (b < 32 && b != '\n' && b != '\r' && b != '\t') {
			return nil, ErrProfile
		}
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, ErrProfile
	}
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, ErrProfile
		}
	}
	reader := csv.NewReader(bytes.NewReader(body))
	reader.FieldsPerRecord = 3
	reader.TrimLeadingSpace = true
	devices := []GPUDevice{}
	seen := map[string]bool{}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(devices) >= 32 {
			return nil, ErrProfile
		}
		id := strings.TrimSpace(row[0])
		if !nvidiaUUID(id) || seen[strings.ToLower(id)] {
			return nil, ErrProfile
		}
		total, ok := nvidiaMiB(row[1])
		if !ok || total == 0 {
			return nil, ErrProfile
		}
		free, ok := nvidiaMiB(row[2])
		if !ok || free > total {
			return nil, ErrProfile
		}
		seen[strings.ToLower(id)] = true
		devices = append(devices, GPUDevice{ID: id, Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: total, AvailableBytes: free})
	}
	if len(devices) == 0 {
		return nil, ErrProfile
	}
	return devices, nil
}

func nvidiaUUID(id string) bool {
	if !strings.HasPrefix(id, "GPU-") || len(id) < 12 || len(id) > 84 {
		return false
	}
	hex := func(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' }
	suffix := id[4:]
	if !hex(suffix[0]) || !hex(suffix[len(suffix)-1]) {
		return false
	}
	for _, b := range []byte(suffix) {
		if b != '-' && !hex(b) {
			return false
		}
	}
	return true
}

func nvidiaMiB(text string) (uint64, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	for _, b := range []byte(text) {
		if b < '0' || b > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil || value > math.MaxUint64>>20 {
		return 0, false
	}
	return value << 20, true
}
