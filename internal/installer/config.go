package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"go.yaml.in/yaml/v3"
)

// ConfigPath is where setup saves the owner's answers. Re-running setup reads
// them as defaults; `linx setup --config` uses a file without asking.
const ConfigPath = "/etc/linx/setup.yaml"

// ProfileAuto lets setup pick the resource profile from the hardware.
const ProfileAuto = "auto"

// Config is setup.yaml. Later phases add domain, deployment profile, etc.
type Config struct {
	Version int          `yaml:"version"`
	Docker  DockerConfig `yaml:"docker"`
	// ContainerUI is "none" or "portainer".
	ContainerUI string `yaml:"container_ui"`
	// ResourceProfile is "auto", "lite", "standard" or "performance".
	ResourceProfile string `yaml:"resource_profile"`
}

// DockerConfig controls the Docker prerequisite.
type DockerConfig struct {
	// Install allows setup to install or upgrade Docker from Docker's official
	// repository when it's missing or too old.
	Install bool `yaml:"install"`
}

// DefaultConfig is used when there are no saved answers.
func DefaultConfig() Config {
	return Config{Version: 1, ContainerUI: ContainerUINone, ResourceProfile: ProfileAuto}
}

// ParseConfig reads setup.yaml. Unknown keys are errors so typos don't pass
// silently. Omitted keys keep their defaults.
func ParseConfig(r io.Reader) (Config, error) {
	c := DefaultConfig()
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("setup.yaml: %w", err)
	}
	return c, c.Validate()
}

// Validate checks every field.
func (c Config) Validate() error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("version: must be 1, got %d", c.Version))
	}
	if !slices.Contains(ContainerUIs, c.ContainerUI) {
		errs = append(errs, fmt.Errorf("container_ui: must be one of %v, got %q", ContainerUIs, c.ContainerUI))
	}
	if c.ResourceProfile != ProfileAuto && !slices.Contains(Profiles, c.ResourceProfile) {
		errs = append(errs, fmt.Errorf("resource_profile: must be auto or one of %v, got %q", Profiles, c.ResourceProfile))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("setup.yaml: %w", err)
	}
	return nil
}

// Marshal renders the config with explanatory comments.
func (c Config) Marshal() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, `# Linx setup answers. Written by linx setup; safe to edit.
# Re-run "sudo linx setup" to change them, or "sudo linx setup --config FILE" to apply a file without questions.
version: %d
docker:
  # Allow setup to install or upgrade Docker from Docker's official repository.
  install: %t
# Optional container management screen: none or portainer.
container_ui: %s
# Resource profile: auto (chosen from the hardware), lite, standard or performance.
resource_profile: %s
`, c.Version, c.Docker.Install, c.ContainerUI, c.ResourceProfile)
	return b.Bytes()
}
