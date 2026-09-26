package trunk

import (
	_ "embed"
	"fmt"

	"go.yaml.in/yaml/v3"
)

//go:embed templates.yaml
var templatesYAML []byte

// Template is a provider or peer preset `linx trunk add` (Phase 1D step 4)
// can start from (docs/TRUNKS.md §3). The probe still checks everything a
// template fills in.
type Template struct {
	Name            string   `yaml:"name"`
	Label           string   `yaml:"label"`
	Kind            string   `yaml:"kind"`
	Transport       string   `yaml:"transport"`
	MediaEncryption string   `yaml:"media_encryption"`
	CertTrust       string   `yaml:"cert_trust"`
	DialFormat      string   `yaml:"dial_format"`
	Port            int      `yaml:"port"`
	Codecs          []string `yaml:"codecs"`
	Notes           string   `yaml:"notes"`
}

// Templates is the provider template catalogue, parsed from templates.yaml
// once at package load.
var Templates = mustParseTemplates()

func mustParseTemplates() []Template {
	var t []Template
	if err := yaml.Unmarshal(templatesYAML, &t); err != nil {
		panic(fmt.Sprintf("trunk: templates.yaml: %v", err))
	}
	return t
}

// TemplateByName returns the template named name, or false if there is none.
func TemplateByName(name string) (Template, bool) {
	for _, t := range Templates {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}
