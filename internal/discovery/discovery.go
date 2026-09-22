// Package discovery finds OPC UA servers on the local network so instruments
// do not have to be typed in by hand. Everything here is read-only: it opens a
// TCP connection, asks the server which endpoints it offers, and closes again.
package discovery

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// Ports probed on every address. 4840 is the registered OPC UA port; the others
// are the usual alternatives, including the LADS reference server's port.
var DefaultPorts = []int{4840, 4841, 4842, 4843, 26543, 48010, 62541}

// Found is one OPC UA server that answered.
type Found struct {
	EndpointURL    string `json:"endpoint_url"`
	Address        string `json:"address"`
	ServerName     string `json:"server_name,omitempty"`
	ApplicationURI string `json:"application_uri,omitempty"`
	// Secure is true when the server offers an encrypted endpoint the
	// connector supports, with either certificate or user-name login.
	Secure bool `json:"secure"`
	// SignOnly is true when the best supported endpoint only signs messages
	// instead of encrypting them; usable after the explicit opt-in.
	SignOnly bool `json:"sign_only,omitempty"`
	// Policies lists the supported encryption policies the server offers.
	Policies []string `json:"policies,omitempty"`
	// Logins lists the login types the server accepts ("certificate", "user name").
	Logins []string `json:"logins,omitempty"`
	// Note explains a server that answered but cannot be used as configured.
	Note string `json:"note,omitempty"`
}

// Options tunes a scan.
type Options struct {
	// Extra addresses ("host" or "host:port") probed in addition to the local
	// subnets, for instruments on a different network.
	Extra []string
	Ports []int
	// Timeout for the whole scan.
	Timeout time.Duration
	// DialTimeout per address.
	DialTimeout time.Duration
}

// Scan probes the local /24 networks plus any extra addresses and returns the
// OPC UA servers that answered.
func Scan(ctx context.Context, opt Options) ([]Found, error) {
	if len(opt.Ports) == 0 {
		opt.Ports = DefaultPorts
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 75 * time.Second
	}
	if opt.DialTimeout <= 0 {
		opt.DialTimeout = 900 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	targets := Targets(opt.Extra, opt.Ports)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no local network found to search — enter the instrument address by hand")
	}

	// Probe in parallel; a lab subnet is 254 addresses per port.
	sem := make(chan struct{}, 256)
	var (
		mu   sync.Mutex
		open []string
		wg   sync.WaitGroup
	)
	for _, t := range targets {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			d := net.Dialer{Timeout: opt.DialTimeout}
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				return
			}
			_ = conn.Close()
			mu.Lock()
			open = append(open, addr)
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	// Ask each open port whether it really speaks OPC UA.
	var (
		out  []Found
		wg2  sync.WaitGroup
		sem2 = make(chan struct{}, 16)
	)
	for _, addr := range open {
		wg2.Add(1)
		go func(addr string) {
			defer wg2.Done()
			sem2 <- struct{}{}
			defer func() { <-sem2 }()
			f, ok := Identify(ctx, addr)
			if !ok {
				return
			}
			mu.Lock()
			out = append(out, f)
			mu.Unlock()
		}(addr)
	}
	wg2.Wait()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Secure != out[j].Secure {
			return out[i].Secure
		}
		return out[i].EndpointURL < out[j].EndpointURL
	})
	return out, nil
}

// Identify asks one address for its OPC UA endpoints.
func Identify(ctx context.Context, addr string) (Found, bool) {
	url := "opc.tcp://" + addr
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	eps, err := opcua.GetEndpoints(ctx, url)
	if err != nil || len(eps) == 0 {
		return Found{}, false
	}

	f := Found{EndpointURL: url, Address: addr}
	for _, ep := range eps {
		if ep.Server != nil {
			if ep.Server.ApplicationName != nil && ep.Server.ApplicationName.Text != "" {
				f.ServerName = ep.Server.ApplicationName.Text
			}
			if ep.Server.ApplicationURI != "" {
				f.ApplicationURI = ep.Server.ApplicationURI
			}
		}
		if ep.SecurityMode != ua.MessageSecurityModeSignAndEncrypt {
			continue
		}
		for _, t := range ep.UserIdentityTokens {
			if t.TokenType == ua.UserTokenTypeCertificate {
				f.Secure = true
			}
		}
	}
	if !f.Secure {
		f.Note = "offers no encrypted endpoint with certificate login — the connector cannot use it as it is configured"
	}
	return f, true
}

// Targets expands the local /24 networks and the extra hints into
// "host:port" strings.
func Targets(extra []string, ports []int) []string {
	seen := map[string]bool{}
	var out []string
	add := func(addr string) {
		if seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, addr)
	}

	for _, e := range extra {
		e = strings.TrimSpace(strings.TrimPrefix(e, "opc.tcp://"))
		if e == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(e); err == nil {
			add(e)
			continue
		}
		for _, p := range ports {
			add(net.JoinHostPort(e, fmt.Sprint(p)))
		}
	}

	for _, host := range subnetHosts() {
		for _, p := range ports {
			add(net.JoinHostPort(host, fmt.Sprint(p)))
		}
	}
	return out
}

// subnetHosts returns every IPv4 address of the local /24 networks. Larger
// networks are narrowed to the /24 around this machine, so a scan stays a
// matter of seconds instead of hours.
func subnetHosts() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			base := append(net.IP(nil), ip...)
			for i := 1; i < 255; i++ {
				h := append(net.IP(nil), base...)
				h[3] = byte(i)
				if h.Equal(ip) {
					continue
				}
				out = append(out, h.String())
			}
		}
	}
	return out
}
