package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"linxpbx.com/linx/internal/dnsapi"
	"net/http"
	"net/url"
	"strconv"
)

const digitalOceanAPI = "https://api.digitalocean.com/v2"

// digitalOcean is Linx's own DigitalOcean client: its library brings
// MPL-2.0 code with it (docs/DECISIONS.md ADR-063).
type digitalOcean struct {
	http  *http.Client
	base  string
	token string
	ttl   int // seconds
}

type doRecord struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Data string `json:"data"`
}

func (d *digitalOcean) records(ctx context.Context, zone, name, typ string) ([]doRecord, error) {
	q := url.Values{"type": {typ}, "name": {dnsapi.Absolute(name, zone)}, "per_page": {"200"}}
	var res struct {
		Records []doRecord `json:"domain_records"`
	}
	if err := d.do(ctx, http.MethodGet, "/domains/"+url.PathEscape(zone)+"/records?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	return res.Records, nil
}

func (d *digitalOcean) Get(ctx context.Context, zone, name, typ string) ([]string, error) {
	recs, err := d.records(ctx, zone, name, typ)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Data)
	}
	return out, nil
}

// Set changes the records there are, adds what's missing and removes the
// rest: DigitalOcean has no record sets, only records.
func (d *digitalOcean) Set(ctx context.Context, zone, name, typ string, values []string) error {
	recs, err := d.records(ctx, zone, name, typ)
	if err != nil {
		return err
	}
	base := "/domains/" + url.PathEscape(zone) + "/records"
	for i, v := range values {
		body := map[string]any{"type": typ, "name": name, "data": v, "ttl": d.ttl}
		if i < len(recs) {
			if recs[i].Data == v {
				continue
			}
			err = d.do(ctx, http.MethodPut, base+"/"+strconv.FormatInt(recs[i].ID, 10), body, nil)
		} else {
			err = d.do(ctx, http.MethodPost, base, body, nil)
		}
		if err != nil {
			return err
		}
	}
	for _, r := range recs[min(len(values), len(recs)):] {
		if err := d.do(ctx, http.MethodDelete, base+"/"+strconv.FormatInt(r.ID, 10), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (d *digitalOcean) Delete(ctx context.Context, zone, name, typ string) error {
	return d.Set(ctx, zone, name, typ, nil)
}

func (d *digitalOcean) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("DigitalOcean: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &e) == nil && e.Message != "" {
			return fmt.Errorf("DigitalOcean: %s", e.Message)
		}
		return fmt.Errorf("DigitalOcean: %s", resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("DigitalOcean: unexpected answer (%s)", resp.Status)
		}
	}
	return nil
}
