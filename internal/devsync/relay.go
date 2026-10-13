package devsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Relay limits, the same as relay/worker.js.
const (
	MaxPairBlob  = 8 * 1024
	MaxGroupBlob = 256 * 1024
)

var (
	errNotFound = errors.New("not found")
	errConflict = errors.New("changed in the meantime")
	errRefused  = errors.New("refused by the relay")
)

// relay is the HTTP client of relay/worker.js (docs/sync.md, Relay).
type relay struct {
	base   string
	client *http.Client
}

func newRelay(base string) *relay {
	return &relay{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 30 * time.Second}}
}

func (r *relay) do(ctx context.Context, method, path string, body []byte, hdr map[string]string) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrOffline, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxGroupBlob+1))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrOffline, err)
	}
	return resp, data, nil
}

func statusError(resp *http.Response) error {
	switch resp.StatusCode {
	case 404:
		return errNotFound
	case 409, 410, 412:
		return errConflict
	case 401, 403, 413:
		return errRefused
	}
	return fmt.Errorf("relay answered %s", resp.Status)
}

// putPair stores a blob in a pairing slot; it is gone after one read or ten minutes.
func (r *relay) putPair(ctx context.Context, id string, blob []byte) error {
	resp, _, err := r.do(ctx, "PUT", "/v1/pair/"+id, blob, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return statusError(resp)
	}
	return nil
}

// takePair reads and removes a pairing slot; errNotFound when it is empty.
func (r *relay) takePair(ctx context.Context, id string) ([]byte, error) {
	resp, data, err := r.do(ctx, "GET", "/v1/pair/"+id, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, statusError(resp)
	}
	return data, nil
}

// getGroup returns the group's blob and version; errNotFound before the first write.
func (r *relay) getGroup(ctx context.Context, g group) ([]byte, int64, error) {
	resp, data, err := r.do(ctx, "GET", "/v1/group/"+g.id(), nil, map[string]string{"Authorization": "Bearer " + g.token()})
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		return nil, 0, statusError(resp)
	}
	version, err := strconv.ParseInt(strings.Trim(resp.Header.Get("ETag"), `"W/`), 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("relay sent no version")
	}
	return data, version, nil
}

// putGroup writes the group's blob if it is still at version (0: create); errConflict
// when another device wrote first or the group is closed. final closes it after this write.
// It returns the new version.
func (r *relay) putGroup(ctx context.Context, g group, version int64, blob []byte, final bool) (int64, error) {
	hdr := map[string]string{"Authorization": "Bearer " + g.token()}
	if final {
		hdr["Sync-Final"] = "1"
	}
	if version == 0 {
		hdr["If-None-Match"] = "*"
	} else {
		hdr["If-Match"] = `"` + strconv.FormatInt(version, 10) + `"`
	}
	resp, _, err := r.do(ctx, "PUT", "/v1/group/"+g.id(), blob, hdr)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode/100 != 2 {
		return 0, statusError(resp)
	}
	next, err := strconv.ParseInt(strings.Trim(resp.Header.Get("ETag"), `"W/`), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("relay sent no version")
	}
	return next, nil
}
