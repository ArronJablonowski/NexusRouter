// Command qualify-cgroup verifies output captured by qualify-linux-cgroup.sh.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: qualify-cgroup REPORT")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err == nil {
		defer f.Close()
		err = verify(f)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Linux cgroup qualification failed:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: live cgroup-v2 memory and CPU limits observed by darwin resources")
}

func verify(input io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(body) > 65536 {
		return errors.New("report exceeds bounds or cannot be read")
	}
	r := bufio.NewReader(strings.NewReader(string(body)))
	line, err := r.ReadString('\n')
	if err != nil {
		return errors.New("missing cgroup observation")
	}
	fields := strings.Fields(line)
	if len(fields) != 6 || fields[0] != "cgroup-v2" {
		return errors.New("invalid cgroup observation")
	}
	values := make([]uint64, 5)
	for i, field := range fields[1:] {
		values[i], err = strconv.ParseUint(field, 10, 64)
		if err != nil {
			return errors.New("non-numeric cgroup limit")
		}
	}
	const memoryLimit = 512 << 20
	if values[0] != memoryLimit || values[1] > memoryLimit || values[2] != 150000 || values[3] != 100000 || values[4] != 0 {
		return errors.New("container did not expose the requested memory, swap, and CPU controls")
	}
	var report struct {
		Source                 string
		TotalRAM, AvailableRAM *uint64
		CPUs                   int `json:"cpu_threads"`
	}
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&report); err != nil {
		return errors.New("invalid resources JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("unexpected trailing report")
	}
	if report.Source != "linux-proc-cgroup-v2" || report.TotalRAM == nil || report.AvailableRAM == nil || *report.TotalRAM == 0 || *report.TotalRAM > memoryLimit || *report.AvailableRAM > *report.TotalRAM || report.CPUs != 1 {
		return errors.New("resource profile does not honor observed cgroup limits")
	}
	return nil
}
