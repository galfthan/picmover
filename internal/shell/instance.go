package shell

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// InstanceName is the file describing the running instance, in the
// config directory.
const InstanceName = "instance.json"

// ErrRunning is returned by lockInstance when another instance holds the
// single-instance lock.
var ErrRunning = errors.New("picmover-app is already running")

// Instance is instance.json: how a second instance reaches the first.
type Instance struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// instanceKey identifies the user's app instance: a hash of the config
// directory, so two config directories (tests, another user) never
// collide.
func instanceKey(configDir string) string {
	sum := sha1.Sum([]byte(strings.ToLower(filepath.Clean(configDir))))
	return hex.EncodeToString(sum[:])
}

// newToken is a random 32-byte hex string.
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// writeInstance writes dir/instance.json (owner-only where the OS has
// modes; it holds the token).
func writeInstance(dir string, in Instance) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, InstanceName), append(b, '\n'), 0o600)
}

// readInstance reads dir/instance.json.
func readInstance(dir string) (Instance, error) {
	var in Instance
	b, err := os.ReadFile(filepath.Join(dir, InstanceName))
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, fmt.Errorf("%s: %w", InstanceName, err)
	}
	if in.Port <= 0 || in.Port > 65535 || in.Token == "" {
		return in, fmt.Errorf("%s: incomplete", InstanceName)
	}
	return in, nil
}

func removeInstance(dir string) { os.Remove(filepath.Join(dir, InstanceName)) }

// postShow asks the instance described by in to show its window: POST
// /api/app/show {token} with the headers every mutating request needs
// (Content-Type: application/json, X-Picmover: 1; no Origin).
func postShow(ctx context.Context, in Instance) error {
	body := `{"token":"` + in.Token + `"}`
	u := "http://127.0.0.1:" + strconv.Itoa(in.Port) + "/api/app/show"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Picmover", "1")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("show: %s", resp.Status)
	}
	return nil
}

// handOff is the second instance's job: read the first one's instance
// file (waiting up to wait for it to appear while the first starts) and
// ask it to show its window.
func handOff(ctx context.Context, configDir string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	var lastErr error
	for {
		in, err := readInstance(configDir)
		if err == nil {
			allowForeground(in.PID)
			if err = postShow(ctx, in); err == nil {
				return nil
			}
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("the running instance did not answer: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
