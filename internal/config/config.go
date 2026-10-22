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

type FileError struct {
	Path   string
	Reason string
	line   int
}

type lineIndex map[string]int

// 这里的collection处理的是顶层的doument的外壳
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
		for i := 0; i+1 < len((n.Content)); i += 2 {
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
	b.WriteString(`"<`)
	b.WriteString(e.File)
	if e.Line == 0 {
		b.WriteString(">: ")
	} else {
		b.WriteString(">:<")
		b.WriteString(strconv.FormatInt(int64(e.Line), 10))
		b.WriteString(">: <")
	}
	b.WriteString(e.Path)
	b.WriteString(">: <")
	b.WriteString(e.Reason)
	b.WriteString(`>"`)

	err := b.String()
	return err
}

var cgnat = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

func (c *Config) Validate() []*FieldError {
	errs := make([]*FieldError, 0, 8)
	add := func(path, format string, a ...any) {
		errs = append(errs, &FieldError{Path: path, Reason: fmt.Sprintf(format, a...)})
	}

	if c.Listen == "" {
		add("listen", "The listen is empty")
	}

	host, portstr, err := net.SplitHostPort(c.Listen)
	if err != nil {
		add("listen", "The %q is illegal", c.Listen)
	}

	port, err := strconv.Atoi(portstr)
	if err != nil {
		add("listen", "The %q switch failed", portstr)
	} else if port < 1 || port > 65535 {
		add("listen", "The %q out of 1~65535", c.Listen)
	}

	if host == "" {
		add("listen", "The %q must not 0.0.0.0", c.Listen)
	}

	if len(c.Routes) == 0 {
		add("routes", "The routes is empty")
	}

	seen := make(map[string]int, len(c.Routes))
	for i, rt := range c.Routes {
		base := fmt.Sprintf("routes[%d]", i)

		if rt.Name == "" {
			add(base+".name", "The name must not be empty")
		} else if first, ok := seen[rt.Name]; ok {
			add(base+".name", "duplicate name %q,first at routes[%d]", rt.Name, first)
		} else {
			seen[rt.Name] = i
		}

		u, err := url.Parse(rt.Upstream)
		if err != nil {
			add(base+".upstream", "The %q upstream parse failed", rt.Name)
			continue
		}
		if !strings.HasSuffix(u.Scheme, "http") && !strings.HasSuffix(u.Scheme, "https") {
			add(base+".upstream", "The %q upstream scheme is illegal", rt.Name)
			continue
		}

		if u.Host == "" {
			add(base+".upstream", "The %q upstream host is illegal", rt.Name)
			continue
		}

	}

	return errs
}

func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("Path is empty")
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
	_ = idx

	data_io := bytes.NewReader(data)
	dec := yaml.NewDecoder(data_io)
	dec.KnownFields(true)

	var cfg Config
	err = dec.Decode(&cfg)
	if errors.Is(err, io.EOF) {
		return nil, errors.New("Config is empty")
	}

	return &cfg, nil
}
