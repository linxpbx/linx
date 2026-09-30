package reach

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"linxpbx.com/linx/internal/dnscheck"
)

// The DNS records by hand (docs/SIMPLER.md §3.1, docs/ui/SCREENS_PHASE1F.md
// §3.1): what to add, where (the DNS company, told by the domain's name
// servers), and what each name shows right now at those name servers.

// What each record is for.
const (
	UseWeb  = "web"  // the domain itself: the web app and sign-in
	UseTURN = "turn" // turn.: call audio through firewalls
	UseSIP  = "sip"  // sip.: desk phones and phone systems at home
)

// Record states.
const (
	RecordOK      = "ok"
	RecordWrong   = "wrong"   // it points somewhere else
	RecordMissing = "missing" // not there yet
	RecordError   = "error"   // the name servers couldn't be asked
	RecordUnknown = "unknown" // there, but Linx doesn't know what it should be
)

// Record is one A record to add, and what it shows now.
type Record struct {
	Use  string `json:"use"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Value is the address it should point at; "" when this network's
	// public address couldn't be found.
	Value string   `json:"value"`
	State string   `json:"state"`
	Seen  []string `json:"seen"`
	// Kept: Linx keeps this record right itself (a DNS token).
	Kept bool `json:"kept"`
}

// Records is the records card.
type Records struct {
	Domain string `json:"domain"`
	// Zone is where the records live ("example.com" for
	// "pbx.example.com"); NameServers are its name servers, and Company
	// the DNS company they belong to ("" when Linx doesn't know it).
	Zone        string    `json:"zone"`
	NameServers []string  `json:"name_servers"`
	Company     string    `json:"company"`
	Records     []Record  `json:"records"`
	CheckedAt   time.Time `json:"checked_at"`
}

// Records lists the records this server needs and checks each at the
// domain's own name servers, all at once.
func (c *Checker) Records(ctx context.Context) Records {
	out := Records{Domain: c.Domain, NameServers: []string{}, Records: []Record{}}
	var (
		wg        sync.WaitGroup
		want      netip.Addr
		wantErr   error
		nsErr     error
		wantKnown = c.Door != "" && c.Door != "none"
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(ctx, Timeout)
		defer cancel()
		out.Zone, out.NameServers, nsErr = c.NameServers(cctx, c.Domain)
		if nsErr != nil || out.NameServers == nil {
			out.NameServers = []string{}
		}
		out.Company = dnscheck.Company(out.NameServers)
	}()
	if wantKnown {
		if c.Door == doorHomeOnly {
			want = c.Home
		} else {
			want, wantErr = c.PublicIP(ctx)
		}
	}
	wg.Wait()

	if wantKnown {
		value := ""
		if wantErr == nil {
			value = want.String()
		}
		out.Records = append(out.Records,
			Record{Use: UseWeb, Name: c.Domain, Value: value},
			Record{Use: UseTURN, Name: "turn." + c.Domain, Value: value})
	}
	if c.atHome() {
		out.Records = append(out.Records, Record{Use: UseSIP, Name: "sip." + c.Domain, Value: c.Home.String()})
	}

	for i := range out.Records {
		r := &out.Records[i]
		r.Type, r.Kept = "A", c.Kept[r.Use]
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, Timeout)
			defer cancel()
			seen, err := c.Lookup(cctx, r.Name)
			r.Seen, r.State = recordState(seen, err, r.Value)
		}()
	}
	wg.Wait()
	out.CheckedAt = c.Now().UTC().Truncate(time.Second)
	return out
}

// atHome reports whether this server is on a home or office network, where
// desk phones find it at sip.<domain> (LINX_SIP_ADDRESS is 127.0.0.1 on a
// rented server).
func (c *Checker) atHome() bool {
	return c.Home.IsValid() && c.Home.Is4() && c.Home.IsPrivate()
}

func recordState(seen []string, err error, want string) ([]string, string) {
	if seen == nil {
		seen = []string{}
	}
	switch {
	case err != nil:
		return seen, RecordError
	case len(seen) == 0:
		return seen, RecordMissing
	case want == "":
		return seen, RecordUnknown
	case len(seen) == 1 && seen[0] == want:
		return seen, RecordOK
	}
	return seen, RecordWrong
}
