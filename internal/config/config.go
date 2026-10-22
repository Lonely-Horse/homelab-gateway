package config

import (
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

type Timeouts struct {
	Dial           time.Time `json:"dial"`
	ResponseHeader time.Time `json:"response_header"`
	Idle           time.Time `json:"idle"`
}

type ServerTimeouts struct {
	ReadHeader time.Duration `yaml:"read_header"`
	Write      time.Time     `yaml:"write"`
	Idle       time.Time     `yaml:"idle"`
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

	}
}

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

func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("The path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, err
	}

	return nil, nil
}
