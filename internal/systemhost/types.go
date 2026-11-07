// Package systemhost separates privileged host administration from the gateway.
package systemhost

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"regexp"
	"time"
)

var ErrInvalid = errors.New("invalid host operation")
var ErrConflict = errors.New("host configuration changed or operation pending")
var ErrUnavailable = errors.New("host agent unavailable")

type Metrics struct {
	Scope               string    `json:"scope"`
	SampledAt           time.Time `json:"sampled_at"`
	CPUPercent          *float64  `json:"cpu_percent"`
	CPUCount            int       `json:"cpu_count"`
	Load1               float64   `json:"load1"`
	MemoryTotal         uint64    `json:"memory_total"`
	MemoryAvailable     uint64    `json:"memory_available"`
	CgroupMemoryLimit   *uint64   `json:"cgroup_memory_limit"`
	CgroupMemoryCurrent *uint64   `json:"cgroup_memory_current"`
	CgroupCPUQuota      *float64  `json:"cgroup_cpu_quota"`
	Interfaces          []Link    `json:"interfaces"`
	DNS                 []string  `json:"dns"`
}
type Link struct {
	Name      string   `json:"name"`
	Up        bool     `json:"up"`
	MTU       int      `json:"mtu"`
	Addresses []string `json:"addresses"`
	RXBytes   uint64   `json:"rx_bytes"`
	TXBytes   uint64   `json:"tx_bytes"`
}
type IPv4 struct {
	Method       string   `json:"method"`
	Addresses    []string `json:"addresses"`
	Gateway      string   `json:"gateway"`
	DNS          []string `json:"dns"`
	AutomaticDNS bool     `json:"automatic_dns"`
}
type Network struct {
	Interface string `json:"interface"`
	Revision  string `json:"revision"`
	IPv4      IPv4   `json:"ipv4"`
}
type Change struct {
	ID               string `json:"id"`
	Interface        string `json:"interface"`
	ExpectedRevision string `json:"expected_revision"`
	IPv4             IPv4   `json:"ipv4"`
}
type Result struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Deadline  time.Time `json:"deadline"`
	Interface string    `json:"interface"`
}
type Snapshot struct {
	Node           string    `json:"node"`
	Metrics        Metrics   `json:"metrics"`
	NetworkEnabled bool      `json:"network_enabled"`
	Networks       []Network `json:"networks"`
	Pending        *Result   `json:"pending"`
}
type Agent interface {
	Snapshot(context.Context) (Snapshot, error)
	Apply(context.Context, Change) (Result, error)
	Finish(context.Context, string, bool) (Result, error)
}

var ifacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`)
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)

func ValidInterface(s string) bool { return ifacePattern.MatchString(s) && s != "lo" }
func (c Change) Validate() error {
	if !idPattern.MatchString(c.ID) || !ValidInterface(c.Interface) || len(c.ExpectedRevision) != 64 {
		return ErrInvalid
	}
	if _, e := hex.DecodeString(c.ExpectedRevision); e != nil {
		return ErrInvalid
	}
	if c.IPv4.Method != "auto" && c.IPv4.Method != "manual" {
		return ErrInvalid
	}
	if len(c.IPv4.Addresses) > 8 || len(c.IPv4.DNS) > 4 {
		return ErrInvalid
	}
	if c.IPv4.Method == "auto" && (len(c.IPv4.Addresses) != 0 || c.IPv4.Gateway != "") {
		return ErrInvalid
	}
	if c.IPv4.Method == "manual" && len(c.IPv4.Addresses) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, s := range c.IPv4.Addresses {
		p, e := netip.ParsePrefix(s)
		if e != nil || !p.Addr().Is4() || !p.Addr().IsGlobalUnicast() || seen[s] {
			return ErrInvalid
		}
		seen[s] = true
	}
	validAddress := func(s string) bool { a, e := netip.ParseAddr(s); return e == nil && a.Is4() && a.IsGlobalUnicast() }
	if c.IPv4.Gateway != "" && !validAddress(c.IPv4.Gateway) {
		return ErrInvalid
	}
	dnsSeen := map[string]bool{}
	for _, s := range c.IPv4.DNS {
		if !validAddress(s) || dnsSeen[s] {
			return ErrInvalid
		}
		dnsSeen[s] = true
	}
	if !c.IPv4.AutomaticDNS && len(c.IPv4.DNS) == 0 {
		return ErrInvalid
	}
	return nil
}
