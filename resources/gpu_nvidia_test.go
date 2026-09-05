package resources

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestParseNVIDIAGPUs(t *testing.T) {
	devices, err := parseNVIDIAGPUs([]byte("GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, 24576, 23000\r\nGPU-12345678-abcd-abcd-abcd-123456789abc, 8192, 0\r\n"))
	if err != nil || len(devices) != 2 {
		t.Fatal(devices, err)
	}
	if devices[0].TotalBytes != 24576<<20 || devices[0].AvailableBytes != 23000<<20 || devices[0].Source != "nvidia-smi" || devices[0].Vendor != "nvidia" || devices[1].AvailableBytes != 0 {
		t.Fatal(devices)
	}
}

func TestParseNVIDIARejectsPartialAndMalformed(t *testing.T) {
	valid := "GPU-12345678-abcd-abcd-abcd-123456789abc, 8192, 4096"
	for _, body := range []string{"", "No devices were found", valid + "\nGPU-deadbeef, N/A, N/A", valid + "\n" + valid, "GPU-deadbeef, 1.5, 1", "GPU-deadbeef, +1, 0", "GPU-deadbeef, -1, 0", "GPU-deadbeef, 1 MiB, 0", "GPU-deadbeef, 0, 0", "GPU-deadbeef, 1, 2", "GPU-deadbeef, 1", "GPU-deadbeef, 1, 0, extra", "GPU-../../etc, 1, 0", "MIG-deadbeef, 1, 0", valid + "\n\n" + valid, strings.Repeat("x", 65537), "GPU-deadbeef, 18446744073709551616, 0", fmt.Sprintf("GPU-deadbeef, %d, 0", uint64(math.MaxUint64>>20)+1), "GPU-deadbeef, 1, \xff"} {
		devices, err := parseNVIDIAGPUs([]byte(body))
		if devices != nil || !errors.Is(err, ErrProfile) {
			t.Fatalf("accepted malformed input: %v %v", devices, err)
		}
	}
}

func TestParseNVIDIABounds(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 32; i++ {
		fmt.Fprintf(&body, "GPU-%08x, 1, 0\n", i)
	}
	if devices, err := parseNVIDIAGPUs([]byte(body.String())); err != nil || len(devices) != 32 {
		t.Fatal(devices, err)
	}
	body.WriteString("GPU-abcdefab, 1, 0\n")
	if _, err := parseNVIDIAGPUs([]byte(body.String())); !errors.Is(err, ErrProfile) {
		t.Fatal(err)
	}
	max := uint64(math.MaxUint64 >> 20)
	devices, err := parseNVIDIAGPUs([]byte(fmt.Sprintf("GPU-deadbeef, %d, %d", max, max)))
	if err != nil || devices[0].TotalBytes != max<<20 {
		t.Fatal(devices, err)
	}
}
