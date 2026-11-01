package plugins

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var optionKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseOption reads key=value. The value becomes a boolean or a number if it reads
// as one, and otherwise stays a string. An empty value is nil, which removes the
// option.
func ParseOption(arg string) (key string, value any, err error) {
	key, text, found := strings.Cut(arg, "=")
	if !found || !optionKey.MatchString(key) {
		return "", nil, fmt.Errorf("%q is not key=value, with a key of letters, digits and underscores", arg)
	}
	if text == "" {
		return key, nil, nil
	}
	if text == "true" || text == "false" {
		return key, text == "true", nil
	}
	if number, err := strconv.ParseFloat(text, 64); err == nil {
		return key, number, nil
	}
	return key, text, nil
}

// SetOptions changes a plugin's options. A nil value removes one. Rex's checker reads
// the plugin with the new options, and a failure changes nothing. Event scripts
// restart with them.
func (m *Manager) SetOptions(ctx context.Context, id string, changes map[string]any) error {
	lock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()

	current, layout, err := m.loadSetUp()
	if err != nil {
		return err
	}
	p, ok := current.Plugins[id]
	if !ok {
		return fmt.Errorf("%s is not installed", id)
	}
	client := m.client(current)

	next := *p
	next.Options = map[string]any{}
	maps.Copy(next.Options, p.Options)
	for key, value := range changes {
		if value == nil {
			delete(next.Options, key)
		} else {
			next.Options[key] = value
		}
	}
	next.Generation++
	next.UpdatedAt = time.Now()

	has, err := m.CheckConfigCode(ctx, current, &next)
	if err != nil {
		return pluginOptionsError(id, err)
	}
	previous := *p
	*p = next
	if err := layout.WriteIndex(m.entries(current, layout)); err != nil {
		return err
	}
	if err := client.CheckConfig(ctx, layout.Init); err != nil {
		*p = previous
		if undoErr := layout.WriteIndex(m.entries(current, layout)); undoErr != nil {
			m.printf("warning: could not undo a step: %v", undoErr)
		}
		return pluginOptionsError(id, err)
	}
	if err := current.Save(m.Dirs.StateFile()); err != nil {
		return err
	}
	if has {
		m.reload(ctx, client)
	}
	m.notifyDaemon(false)
	return nil
}

func pluginOptionsError(id string, err error) error {
	return fmt.Errorf("%s does not load with these options, so rexd changed nothing: %w", id, err)
}
