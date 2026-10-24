package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Timeouts struct {
	Dial           time.Duration `yaml:"dial"`
	ResponseHeader time.Duration `yaml:"response_header"`
	Idle           time.Duration `yaml:"idle"`
}

type ServerTimeouts struct {
	ReadHeader time.Duration `yaml:"read_header"`
	Write      time.Duration `yaml:"write"`
	Idle       time.Duration `yaml:"idle"`
}

type Route struct {
	Name        string `yaml:"name"`
	Host        string `yaml:"host"`
	PathPrefix  string `yaml:"path_prefix"`
	Upstream    string `yaml:"upstream"`
	StripPrefix bool   `yaml:"strip_prefix"`
	Health      string `yaml:"health"`
}

type Config struct {
	Listen   string         `yaml:"listen"`
	Timeouts Timeouts       `yaml:"timeouts"`
	Server   ServerTimeouts `yaml:"server"`
	Routes   []Route        `yaml:"routes"`
}

type lineIndex map[string]int

// collectLines 剥掉顶层 Document 外壳，再用 walk 建立“字段路径 → 所在行号”索引
func collectLines(root *yaml.Node) lineIndex {
	idx := lineIndex{}
	if root == nil {
		return idx
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return idx
		}
		root = root.Content[0]
	}
	walk(root, "", idx)

	return idx
}

// 使用递归进入不同层进行扫描，最重要的是要对于node的树状结构要有认知和了解
func walk(n *yaml.Node, prefix string, idx lineIndex) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			path := key.Value
			if prefix != "" {
				path = prefix + "." + key.Value
			}
			idx[path] = val.Line
			walk(val, path, idx)
		}
	case yaml.SequenceNode:
		for i := 0; i < len(n.Content); i++ {
			val := n.Content[i]
			path := prefix + "[" + strconv.Itoa(i) + "]"
			idx[path] = val.Line
			walk(val, path, idx)
		}
	}
}

type FieldError struct {
	File   string
	Line   int
	Path   string
	Reason string
}

func (e *FieldError) Error() string {
	var b strings.Builder
	b.WriteString(e.File)
	b.WriteString(":")
	if e.Line != 0 {
		b.WriteString(strconv.FormatInt(int64(e.Line), 10))
		b.WriteString(": ")
	}
	b.WriteString(e.Path)
	b.WriteString(": ")
	b.WriteString(e.Reason)

	return b.String()
}

var cgnat = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

type addFunc func(path, format string, a ...any)

func (c *Config) validateListen(add addFunc) (string, int) {
	var listenHost string
	var listenPort int

	if c.Listen == "" {
		add("listen", "must not be empty")
	} else if host, portstr, err := net.SplitHostPort(c.Listen); err != nil {
		add("listen", "must be host:port, got %q", c.Listen)
	} else if port, err := strconv.Atoi(portstr); err != nil {
		add("listen", "port %q is not a number", portstr)
	} else if port < 1 || port > 65535 {
		add("listen", "port %d out of range 1-65535", port)
	} else {
		listenHost, listenPort = host, port
	}

	return listenHost, listenPort
}

func (c *Config) validateRoutes(host string, port int, add addFunc) {
	if len(c.Routes) == 0 {
		add("routes", "at least one route is required")
	}

	lip := net.ParseIP(host)
	listenCatchesLoopback := host == "" || (lip != nil && (lip.IsUnspecified() || lip.IsLoopback())) || strings.EqualFold(host, "localhost")
	seen := make(map[string]int, len(c.Routes))
	for i, rt := range c.Routes {
		base := fmt.Sprintf("routes[%d]", i)

		if rt.Name == "" {
			add(base+".name", "must not be empty")
		} else if first, ok := seen[rt.Name]; ok {
			add(base+".name", "duplicate name %q, first defined at routes[%d]", rt.Name, first)
		} else {
			seen[rt.Name] = i
		}

		if rt.StripPrefix && rt.PathPrefix == "" {
			add(base+".path_prefix", "strip_prefix requires a non-empty path_prefix")
		}

		u, err := url.Parse(rt.Upstream)
		if err != nil {
			add(base+".upstream", "cannot parse upstream: %v", err)
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			add(base+".upstream", "scheme must be \"http\" or \"https\", got %q", u.Scheme)
			continue
		}

		if u.Host == "" {
			add(base+".upstream", "upstream must include a host")
			continue
		}

		hostname := u.Hostname()
		if ip := net.ParseIP(hostname); ip != nil {
			allowed := ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)
			if !allowed {
				add(base+".upstream", "non-private IP %s is not allowed", ip)
			}
		}

		upPort := u.Port()
		upPortNum := 0
		if upPort == "" {
			switch u.Scheme {
			case "http":
				upPortNum = 80
			case "https":
				upPortNum = 443
			}
		} else {
			upPortNum, _ = strconv.Atoi(upPort)
		}
		if upPortNum == port {
			self := false
			if ip := net.ParseIP(hostname); ip != nil {
				upIsLoopback := ip.IsLoopback() || ip.IsUnspecified()
				self = upIsLoopback && listenCatchesLoopback
			} else {
				self = strings.EqualFold(hostname, "localhost") && listenCatchesLoopback
			}

			if self || hostname == host {
				add(base+".upstream", "points to the gateway itself (%s), forms a loop",
					net.JoinHostPort(hostname, strconv.Itoa(upPortNum)))
			}

		}
	}

}

func (c *Config) validateTimeouts(add addFunc) {
	type pd struct {
		path string
		dur  time.Duration
	}

	pds := []pd{
		{"timeouts.dial", c.Timeouts.Dial},
		{"timeouts.response_header", c.Timeouts.ResponseHeader},
		{"timeouts.idle", c.Timeouts.Idle},
		{"server.read_header", c.Server.ReadHeader},
		{"server.write", c.Server.Write},
		{"server.idle", c.Server.Idle},
	}

	for _, pd := range pds {
		if pd.dur <= 0 {
			add(pd.path, "must be > 0, got %s", pd.dur)
		}
	}
}

func (c *Config) Validate() []*FieldError {
	errs := make([]*FieldError, 0, 8)
	add := func(path, format string, a ...any) {
		errs = append(errs, &FieldError{Path: path, Reason: fmt.Sprintf(format, a...)})
	}

	listenHost, listenPort := c.validateListen(add)
	c.validateRoutes(listenHost, listenPort, add)
	c.validateTimeouts(add)

	return errs
}

func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var root yaml.Node
	err = yaml.Unmarshal(data, &root)
	if err != nil {
		return nil, err
	}

	idx := collectLines(&root)

	dataIo := bytes.NewReader(data)
	dec := yaml.NewDecoder(dataIo)
	dec.KnownFields(true)

	var cfg Config
	err = dec.Decode(&cfg)
	switch {
	case errors.Is(err, io.EOF):
		return nil, errors.New("config is empty")
	case err != nil:
		return nil, err
	}

	errs := cfg.Validate()
	if len(errs) > 0 {
		joined := make([]error, 0, len(errs))
		for _, fe := range errs {
			fe.File = path
			fe.Line = idx[fe.Path]
			joined = append(joined, fe)
		}
		return nil, errors.Join(joined...)
	}

	return &cfg, nil
}
