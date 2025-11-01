package oci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Spec struct {
	OCIVersion  string            `json:"ociVersion"`
	Process     *Process          `json:"process"`
	Root        *Root             `json:"root"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type Process struct {
	Terminal bool     `json:"terminal"`
	Args     []string `json:"args"`
	Env      []string `json:"env"`
	Cwd      string   `json:"cwd"`
}

type Root struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}

func LoadSpec(bundle string) (*Spec, error) {
	p := filepath.Join(bundle, "config.json")
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open spec: %w", err)
	}
	defer f.Close()
	var s Spec
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode spec: %w", err)
	}
	return &s, nil
}
