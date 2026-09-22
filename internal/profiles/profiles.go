// Package profiles loads vendor/model mapping profiles. A profile says where
// sample ID, method name and operator live in a given vendor's LADS tree, so
// vendor differences stay out of the code.
package profiles

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtin embed.FS

// Profile describes how to read metadata from one vendor's LADS layout.
type Profile struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	Vendor      string `yaml:"vendor"`
	Model       string `yaml:"model"`
	DeviceType  string `yaml:"device_type"`

	// Browse names, relative to the result object, tried in order. The first
	// readable, non-empty value wins.
	SampleCodePaths []string `yaml:"sample_code_paths"`
	MethodPaths     []string `yaml:"method_paths"`
	OperatorPaths   []string `yaml:"operator_paths"`

	// LADS also carries run context as KeyValuePair arrays. These say where
	// those arrays live and which keys to read from them.
	PropertyPaths  []string `yaml:"property_paths"`
	SampleCodeKeys []string `yaml:"sample_code_keys"`
	MethodKeys     []string `yaml:"method_keys"`
	OperatorKeys   []string `yaml:"operator_keys"`

	// Where the measurement series lives.
	SeriesXPaths []string `yaml:"series_x_paths"`
	SeriesYPaths []string `yaml:"series_y_paths"`

	// Containers searched for a numeric array when no series path matches.
	SeriesContainerPaths []string `yaml:"series_container_paths"`

	// Node browse names copied verbatim into summary.
	ScalarPaths []string `yaml:"scalar_paths"`

	// Values considered "finished" for the result state variable.
	FinishedStates []string `yaml:"finished_states"`

	// AmbiguousStates are state names that mean "not running" but not
	// necessarily "a result was produced" (Ready, Idle, ...). They count as
	// finished only when the result also carries a stop timestamp.
	AmbiguousStates []string `yaml:"ambiguous_states"`

	// FinishedStateNumbers are state machine numbers considered finished, for
	// instruments that report CurrentState/Number instead of readable text.
	FinishedStateNumbers []int `yaml:"finished_state_numbers"`

	DefaultUnitX string `yaml:"default_unit_x"`
	DefaultUnitY string `yaml:"default_unit_y"`
}

// Set is a loaded collection of profiles.
type Set struct {
	mu       sync.RWMutex
	profiles map[string]Profile
}

// Load reads the embedded profiles plus any *.yaml in overrideDir (which may
// be empty or missing), letting customers add profiles without a rebuild.
func Load(overrideDir string) (*Set, error) {
	s := &Set{profiles: map[string]Profile{}}

	entries, err := builtin.ReadDir("builtin")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		raw, err := builtin.ReadFile("builtin/" + e.Name())
		if err != nil {
			return nil, err
		}
		if err := s.add(e.Name(), raw); err != nil {
			return nil, err
		}
	}

	if overrideDir != "" {
		files, _ := filepath.Glob(filepath.Join(overrideDir, "*.yaml"))
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			if err := s.add(filepath.Base(f), raw); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *Set) add(filename string, raw []byte) error {
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("profile %s: %w", filename, err)
	}
	if p.ID == "" {
		p.ID = strings.TrimSuffix(filename, ".yaml")
	}
	if len(p.SeriesContainerPaths) == 0 {
		p.SeriesContainerPaths = []string{"VariableSet", "Variables", "Results"}
	}
	if len(p.PropertyPaths) == 0 {
		p.PropertyPaths = []string{"Properties", "Samples"}
	}
	if len(p.SampleCodeKeys) == 0 {
		p.SampleCodeKeys = []string{"SampleId", "SampleID", "SampleCode", "Barcode", "SampleBarcode"}
	}
	if len(p.MethodKeys) == 0 {
		p.MethodKeys = []string{"Method", "MethodName", "ProgramTemplateId", "Assay"}
	}
	if len(p.OperatorKeys) == 0 {
		p.OperatorKeys = []string{"Operator", "User", "UserId"}
	}
	if len(p.FinishedStates) == 0 {
		p.FinishedStates = []string{"Completed", "Finished", "Stopped", "Aborted"}
	}
	s.profiles[p.ID] = p
	return nil
}

// Get returns the profile with the given id, falling back to generic-lads.
func (s *Set) Get(id string) Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.profiles[id]; ok {
		return p
	}
	return s.profiles["generic-lads"]
}

// List returns all profile ids with their descriptions, for the setup UI.
func (s *Set) List() []Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, p)
	}
	return out
}
