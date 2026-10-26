package main

import (
	"os"
	"strconv"
	"strings"
)

type hostCPU struct {
	idle  uint64
	total uint64
}

// readHostCPU sums the aggregate cpu line from /proc/stat.
func readHostCPU() (hostCPU, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return hostCPU{}, false
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return hostCPU{}, false
	}
	var total, idle uint64
	for i, v := range f[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		total += n
		// fields: user nice system idle iowait irq softirq steal ...
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return hostCPU{idle: idle, total: total}, true
}

// readMemUsed returns used memory in bytes (MemTotal - MemAvailable).
func readMemUsed() uint64 {
	m := readMeminfo()
	total := m["MemTotal"]
	avail := m["MemAvailable"]
	if avail == 0 {
		avail = m["MemFree"]
	}
	if total > avail {
		return total - avail
	}
	return 0
}
