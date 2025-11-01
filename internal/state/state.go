package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Status string

const (
	Created Status = "created"
	Running Status = "running"
	Stopped Status = "stopped"
)

type ContainerState struct {
	ID          string            `json:"id"`
	Bundle      string            `json:"bundle"`
	Pid         int               `json:"pid"`
	Status      Status            `json:"status"`
	CreatedAt   time.Time         `json:"createdAt"`
	StartedAt   *time.Time        `json:"startedAt,omitempty"`
	ExitedAt    *time.Time        `json:"exitedAt,omitempty"`
	ExitCode    *int              `json:"exitCode,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	PidFile     string            `json:"pidFile,omitempty"`
}

func dirFor(stateRoot, id string) string {
	return filepath.Join(stateRoot, id)
}

func pathFor(stateRoot, id string) string {
	return filepath.Join(dirFor(stateRoot, id), "state.json")
}

func Exists(stateRoot, id string) bool {
	_, err := os.Stat(pathFor(stateRoot, id))
	return err == nil
}

func Create(stateRoot string, st *ContainerState) error {
	d := dirFor(stateRoot, st.ID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	p := pathFor(stateRoot, st.ID)
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("container %s already exists", st.ID)
	}
	st.CreatedAt = time.Now()
	st.Status = Created
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(st)
}

func Load(stateRoot, id string) (*ContainerState, error) {
	b, err := os.ReadFile(pathFor(stateRoot, id))
	if err != nil {
		return nil, err
	}
	var st ContainerState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func Save(stateRoot string, st *ContainerState) error {
	p := pathFor(stateRoot, st.ID)
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func Delete(stateRoot, id string) error {
	d := dirFor(stateRoot, id)
	if err := os.RemoveAll(d); err != nil {
		return err
	}
	return nil
}

func EnsureStopped(st *ContainerState) error {
	if st.Status == Running {
		return errors.New("container is running")
	}
	return nil
}
