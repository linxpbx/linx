package install

import (
	"strings"

	"linxpbx.com/linx/internal/dnsapi"
)

// DNSKey is a DNS company's key as a page sends it: the company (one of
// dnsapi.Companies; "" keeps the one setup.yaml names) and either its
// form's fields or, as before, one token.
type DNSKey struct {
	Provider string            `json:"provider,omitempty"`
	Token    string            `json:"token,omitempty"`
	Fields   map[string]string `json:"key,omitempty"`
}

// Empty: nothing was given.
func (k DNSKey) Empty() bool { return strings.TrimSpace(k.Token) == "" && len(k.Fields) == 0 }

// Secret is the company k is for (current when it doesn't say) and the key
// as the secret file keeps it (dnsapi.Company.Encode), or a refusal in
// plain words. A lone token's form is checked later, with the company's.
func (k DNSKey) Secret(current string) (provider, secret, refusal string) {
	provider = k.Provider
	if provider == "" {
		provider = current
	}
	c, ok := dnsapi.Find(provider)
	if !ok {
		return provider, "", "Choose your DNS company from the list."
	}
	if len(k.Fields) == 0 {
		return provider, strings.TrimSpace(k.Token), ""
	}
	s, err := c.Encode(k.Fields)
	if err != nil {
		return provider, "", err.Error()
	}
	return provider, s, ""
}
