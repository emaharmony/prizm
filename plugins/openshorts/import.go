package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/emaharmony/prizm/plugins/openshorts/internal/creatorvod"
	"github.com/gofrs/flock"
	"gopkg.in/yaml.v3"
)

const serverName = "openshorts"

type registration struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Enabled bool     `yaml:"enabled"`
}

func importPlugin(configPath, executable, stateDir string) (bool, error) {
	lock := flock.New(configPath + ".openshorts.lock")
	defer lock.Close()
	locked, err := lock.TryLock()
	if err != nil {
		return false, err
	}
	if !locked {
		return false, errors.New("another OpenShorts import is using this configuration")
	}

	info, err := os.Stat(configPath)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("Prizm configuration must be a regular file")
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		return false, err
	}
	wanted := registration{
		Name: serverName, Command: executable, Args: []string{"serve", "--state-dir", stateDir}, Enabled: true,
	}
	next, err := addRegistration(original, wanted)
	if err != nil {
		return false, err
	}
	demo, err := creatorvod.OpenDemo(stateDir)
	if err != nil {
		return false, fmt.Errorf("initialize OpenShorts plugin state: %w", err)
	}
	if err := demo.Close(); err != nil {
		return false, err
	}
	if next == nil {
		return false, nil
	}
	if err := writeConfig(configPath, info.Mode().Perm(), original, next); err != nil {
		return false, fmt.Errorf("save OpenShorts registration: %w", err)
	}
	return true, nil
}

func addRegistration(original []byte, wanted registration) ([]byte, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(original))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("read Prizm configuration: %w", err)
	}
	if err := decoder.Decode(new(yaml.Node)); err != io.EOF {
		return nil, errors.New("Prizm configuration must contain one YAML document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("Prizm configuration must be a YAML mapping")
	}
	// Decode checks duplicate YAML keys, while editing the node retains comments.
	var resolved map[string]any
	if err := document.Decode(&resolved); err != nil {
		return nil, err
	}
	root := document.Content[0]
	var servers *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "mcp_servers" {
			servers = root.Content[i+1]
			break
		}
	}
	if servers == nil {
		if _, inherited := resolved["mcp_servers"]; inherited {
			return nil, errors.New("mcp_servers is inherited through a YAML merge and must be explicit to import OpenShorts")
		}
		servers = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mcp_servers"}, servers)
	}
	if servers.Kind == yaml.ScalarNode && servers.Tag == "!!null" {
		servers.Kind, servers.Tag, servers.Value = yaml.SequenceNode, "!!seq", ""
	}
	if servers.Kind != yaml.SequenceNode {
		return nil, errors.New("mcp_servers must be an explicit sequence")
	}

	matches := 0
	for _, entry := range servers.Content {
		var current registration
		if err := entry.Decode(&current); err != nil {
			return nil, fmt.Errorf("read existing MCP server: %w", err)
		}
		if current.Name != wanted.Name {
			continue
		}
		matches++
		if !reflect.DeepEqual(current, wanted) || matches > 1 {
			return nil, errors.New("mcp_servers already contains a different or duplicate openshorts entry")
		}
	}
	if matches == 1 {
		return nil, nil
	}

	var newEntry yaml.Node
	if err := newEntry.Encode(wanted); err != nil {
		return nil, err
	}
	servers.Style &^= yaml.FlowStyle
	servers.Content = append(servers.Content, &newEntry)
	var result bytes.Buffer
	encoder := yaml.NewEncoder(&result)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	return result.Bytes(), encoder.Close()
}

func writeConfig(path string, mode os.FileMode, original, next []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".openshorts-import-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if _, err := temp.Write(next); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	latest, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(latest, original) {
		return errors.New("Prizm configuration changed during import")
	}
	return os.Rename(temp.Name(), path)
}
