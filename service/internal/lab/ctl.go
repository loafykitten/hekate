package lab

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Host requests: what hekate-labctl does for her (deploy/lab/labctl.sh).
const (
	verbStats     = "stats"
	verbList      = "list"
	verbSnapshot  = "snapshot"
	verbRollback  = "rollback"
	verbIsolate   = "isolate"
	verbReconnect = "reconnect"
)

const ctlPoll = 250 * time.Millisecond

// ctl asks the host for things that need the hypervisor. A request is a file in requests/ that
// hekate-labctl.path notices; the answer appears in results/, which only root can write.
type ctl struct {
	dir string
	now func() time.Time
}

// ctlAnswer is hekate-labctl's reply.
type ctlAnswer struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// errHostSilent means hekate-labctl didn't answer in time.
var errHostSilent = errors.New("the homelab didn't answer the lab request in time (is hekate-labctl.path running?)")

// do sends one request and waits for its answer until ctx ends.
func (c *ctl) do(ctx context.Context, verb, arg string) (json.RawMessage, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("request id: %w", err)
	}
	id := hex.EncodeToString(b[:])
	body, err := json.Marshal(map[string]string{"verb": verb, "arg": arg})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	reqDir := filepath.Join(c.dir, "requests")
	tmp := filepath.Join(reqDir, "."+id+".tmp")
	req := filepath.Join(reqDir, id+".json")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return nil, fmt.Errorf("write the lab request: %w", err)
	}
	if err := os.Rename(tmp, req); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("write the lab request: %w", err)
	}
	res := filepath.Join(c.dir, "results", id+".json")
	t := time.NewTicker(ctlPoll)
	defer t.Stop()
	for {
		raw, err := os.ReadFile(res)
		if err == nil {
			var a ctlAnswer
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("the homelab's answer was unreadable: %w", err)
			}
			if !a.OK {
				return nil, fmt.Errorf("the homelab said: %s", a.Error)
			}
			return a.Data, nil
		}
		select {
		case <-ctx.Done():
			// Take the request back if it was never picked up; the host may still be on it otherwise.
			if os.Remove(req) == nil {
				return nil, errHostSilent
			}
			return nil, fmt.Errorf("the homelab is still working on the lab request: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// hostStats is what the host sees of the lab (hekate-labctl stats). The lab can't change any of it.
type hostStats struct {
	State          string     `json:"state"` // libvirt's: running, shut off, ...
	Isolated       bool       `json:"isolated"`
	IsolatedSince  string     `json:"isolated_since"`
	CPUTimeNS      uint64     `json:"cpu_time_ns"`
	VCPUs          int        `json:"vcpus"`
	RxBytes        uint64     `json:"rx_bytes"`
	TxBytes        uint64     `json:"tx_bytes"`
	SMTPBlocked    uint64     `json:"smtp_blocked"`
	PrivateBlocked uint64     `json:"private_blocked"`
	HostBlocked    uint64     `json:"host_blocked"`
	DiskBytes      uint64     `json:"disk_bytes"`
	Snapshots      []snapshot `json:"snapshots"`
	At             int64      `json:"at"` // unix seconds
}

// snapshot is one of the lab's disk snapshots.
type snapshot struct {
	Name string `json:"name"`
	Date int64  `json:"date"` // unix seconds
}

func (s *Service) stats(ctx context.Context) (hostStats, error) {
	if !s.Configured() {
		return hostStats{}, errNotSetUp
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := s.ctl.do(ctx, verbStats, "")
	if err != nil {
		return hostStats{}, err
	}
	var st hostStats
	if err := json.Unmarshal(raw, &st); err != nil {
		return hostStats{}, fmt.Errorf("the lab's stats were unreadable: %w", err)
	}
	return st, nil
}

// takeSnapshot asks the host for a snapshot labeled label and returns its name.
func (s *Service) takeSnapshot(ctx context.Context, label string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	raw, err := s.ctl.do(ctx, verbSnapshot, label)
	if err != nil {
		return "", err
	}
	var r struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("the snapshot answer was unreadable: %w", err)
	}
	return r.Name, nil
}

func (s *Service) isolate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	_, err := s.ctl.do(ctx, verbIsolate, "")
	return err
}
