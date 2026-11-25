package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"

	"github.com/codeasier/go-router/internal/controlplane"
)

const maxConfigBytes = 64 << 10

var (
	bearerEnvName  = regexp.MustCompile(`^GO_ROUTER_DASHBOARD_KEY_[A-Z0-9_]{1,64}$`)
	errPrivateFile = errors.New("Dashboard inventory must be a private regular file (chmod 600).")
	errReadConfig  = errors.New("Unable to read Dashboard inventory.")
	errInvalidJSON = errors.New("Invalid Dashboard inventory JSON.")
	errMissingKey  = errors.New("Each Dashboard node requires a populated GO_ROUTER_DASHBOARD_KEY_* environment variable.")
)

type inventory struct {
	Nodes []nodeConfig `json:"nodes"`
}

type nodeConfig struct {
	NodeID          string  `json:"node_id"`
	AdminAddr       string  `json:"admin_addr"`
	BearerEnv       string  `json:"bearer_env"`
	DesiredRevision *string `json:"desired_revision"`
}

func loadConfig(path string, lookup func(string) (string, bool)) ([]controlplane.CollectorNode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errReadConfig
	}
	if !isPrivateFile(info) {
		return nil, errPrivateFile
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errReadConfig
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !isPrivateFile(opened) || !os.SameFile(info, opened) {
		return nil, errPrivateFile
	}
	b, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(b) > maxConfigBytes {
		return nil, errReadConfig
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var cfg inventory
	if err := dec.Decode(&cfg); err != nil || len(cfg.Nodes) == 0 {
		return nil, errInvalidJSON
	}
	var tail json.RawMessage
	if err := dec.Decode(&tail); err != io.EOF {
		return nil, errInvalidJSON
	}
	nodes := make([]controlplane.CollectorNode, 0, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		if !bearerEnvName.MatchString(n.BearerEnv) {
			return nil, errMissingKey
		}
		secret, ok := lookup(n.BearerEnv)
		if !ok || secret == "" {
			return nil, errMissingKey
		}
		nodes = append(nodes, controlplane.CollectorNode{
			NodeID: n.NodeID, AdminAddr: n.AdminAddr, BearerKey: secret, DesiredRevision: n.DesiredRevision,
		})
	}
	return nodes, nil
}

func isPrivateFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0
}
