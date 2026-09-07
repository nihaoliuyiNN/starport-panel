package agent

import (
	"bufio"
	"math"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// collectFacts 采集本机现状；尽量零依赖（读 /proc + net 标准库），非 Linux 上退化为空值。
func collectFacts() Facts {
	f := Facts{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
	}
	if h, err := os.Hostname(); err == nil {
		f.Hostname = h
	}
	f.InternalIP = internalIP()
	f.Kernel = kernelRelease()
	f.MemBytes = memTotalBytes()
	f.CPUUsedPercent = cpuUsedPercent()
	f.MemUsedPercent = memUsedPercent()
	return f
}

// CPU 采样基线：跨两次心跳算差值，避免每次都阻塞采样；首次无基线时短采一次拿即时值。
var (
	cpuMu       sync.Mutex
	cpuPrevBusy uint64
	cpuPrevTot  uint64
	cpuHavePrev bool
)

// cpuUsedPercent 基于 /proc/stat 两次采样的 busy/total 差值算 CPU 利用率（0-100）。
// 首次调用无基线：短采样 200ms 取即时值；之后用与上次的差值，不再阻塞。
func cpuUsedPercent() float64 {
	busy, total, ok := readCPUSample()
	if !ok {
		return 0
	}
	cpuMu.Lock()
	defer cpuMu.Unlock()
	if !cpuHavePrev {
		time.Sleep(200 * time.Millisecond)
		b2, t2, ok2 := readCPUSample()
		if !ok2 {
			cpuPrevBusy, cpuPrevTot, cpuHavePrev = busy, total, true
			return 0
		}
		cpuPrevBusy, cpuPrevTot, cpuHavePrev = b2, t2, true
		return cpuDelta(busy, total, b2, t2)
	}
	p := cpuDelta(cpuPrevBusy, cpuPrevTot, busy, total)
	cpuPrevBusy, cpuPrevTot = busy, total
	return p
}

func cpuDelta(b1, t1, b2, t2 uint64) float64 {
	dt := float64(t2 - t1)
	if dt <= 0 {
		return 0
	}
	p := float64(b2-b1) / dt * 100
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return round1(p)
}

// readCPUSample 读 /proc/stat 首行 "cpu ..."，返回 busy(总-空闲) 与 total 时钟数。
func readCPUSample() (busy, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := string(b)
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var idle uint64
	for i := 1; i < len(fields); i++ {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			continue
		}
		total += v
		if i == 4 || i == 5 { // idle + iowait 计入空闲
			idle += v
		}
	}
	if total < idle {
		return 0, 0, false
	}
	return total - idle, total, true
}

// memUsedPercent 读 /proc/meminfo：(MemTotal - MemAvailable) / MemTotal * 100。
func memUsedPercent() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	var total, avail uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			total, haveTotal = parseMeminfoKB(line), true
		} else if strings.HasPrefix(line, "MemAvailable:") {
			avail, haveAvail = parseMeminfoKB(line), true
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if !haveTotal || !haveAvail || total == 0 || avail > total {
		return 0
	}
	return round1(float64(total-avail) / float64(total) * 100)
}

func parseMeminfoKB(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(fields[1], 10, 64)
	return v
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// internalIP 选一个内网 IPv4：优先私有网段（10/172.16-31/192.168），否则退回首个非回环 IPv4。
func internalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var fallback string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4.IsPrivate() {
			return ip4.String()
		}
		if fallback == "" {
			fallback = ip4.String()
		}
	}
	return fallback
}

// kernelRelease 读内核版本（Linux：/proc/sys/kernel/osrelease）。
func kernelRelease() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// memTotalBytes 读物理内存总量（Linux：/proc/meminfo 的 MemTotal，单位 kB）。
func memTotalBytes() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
