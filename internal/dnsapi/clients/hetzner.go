package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"linxpbx.com/linx/internal/dnsapi"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// hetznerAPI is the Hetzner Console's DNS (the Cloud API; the old DNS
// Console's own API is being retired).
const hetznerAPI = "https://api.hetzner.cloud/v1"

// hetzner is Linx's own Hetzner client: one record set (name and type) at
// a time, by the zone's name.
type hetzner struct {
	http  *http.Client
	base  string
	token string
	ttl   int // seconds
}

type hetznerRRSet struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     *int   `json:"ttl,omitempty"`
	Records []struct {
		Value string `json:"value"`
	} `json:"records"`
}

type hetznerValue struct {
	Value string `json:"value"`
}

// errNotFound is a 404: no such record set.
var errNotFound = errors.New("not found")

func (h *hetzner) rrset(zone, name, typ string) string {
	return "/zones/" + url.PathEscape(zone) + "/rrsets/" + url.PathEscape(name) + "/" + typ
}

func (h *hetzner) Get(ctx context.Context, zone, name, typ string) ([]string, error) {
	var res struct {
		RRSet hetznerRRSet `json:"rrset"`
	}
	err := h.do(ctx, http.MethodGet, h.rrset(zone, name, typ), nil, &res)
	if errors.Is(err, errNotFound) {
		// No such record set, or no such zone for this token: tell them apart.
		if err := h.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zone), nil, nil); errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("Hetzner: this token can't see a zone %s", zone)
		} else if err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range res.RRSet.Records {
		out = append(out, unquoteTXT(typ, r.Value))
	}
	return out, nil
}

func (h *hetzner) Set(ctx context.Context, zone, name, typ string, values []string) error {
	if len(values) == 0 {
		return h.Delete(ctx, zone, name, typ)
	}
	recs := make([]hetznerValue, len(values))
	for i, v := range values {
		recs[i] = hetznerValue{quoteTXT(typ, v)}
	}
	have, err := h.Get(ctx, zone, name, typ)
	if err != nil {
		return err
	}
	if len(have) > 0 {
		return h.do(ctx, http.MethodPost, h.rrset(zone, name, typ)+"/actions/set_records", map[string]any{"records": recs}, nil)
	}
	return h.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zone)+"/rrsets",
		map[string]any{"name": name, "type": typ, "ttl": h.ttl, "records": recs}, nil)
}

func (h *hetzner) Delete(ctx context.Context, zone, name, typ string) error {
	err := h.do(ctx, http.MethodDelete, h.rrset(zone, name, typ), nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (h *hetzner) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return fmt.Errorf("Hetzner: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("Hetzner: %s", e.Error.Message)
		}
		return fmt.Errorf("Hetzner: %s", resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("Hetzner: unexpected answer (%s)", resp.Status)
		}
	}
	return nil
}

// quoteTXT: Hetzner and Route 53 want TXT values in quotes, as zone files
// write them.
func quoteTXT(typ, v string) string {
	if typ != dnsapi.TypeTXT {
		return v
	}
	return strconv.Quote(v)
}

func unquoteTXT(typ, v string) string {
	if typ != dnsapi.TypeTXT {
		return v
	}
	if u, err := strconv.Unquote(v); err == nil {
		return u
	}
	return strings.Trim(v, `"`)
}
