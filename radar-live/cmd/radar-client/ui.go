package main

import (
	"context"
	"strings"

	"radarlive/internal/client"
)

// ui bundles the UI server with the token that opens it.
type ui struct {
	server *client.UIServer
	token  string
}

// newUI loads the saved settings and builds the UI server. quit ends the program (the Quit button).
func newUI(cfg config, d deps, session *client.Session, quit context.CancelFunc) (*ui, error) {
	settingsPath := cfg.settingsPath
	if settingsPath == "" {
		path, err := client.DefaultSettingsPath()
		if err != nil {
			return nil, err
		}
		settingsPath = path
	}
	settings, loadErr := client.LoadSettings(settingsPath, client.DefaultSettings(d.lookup))
	warning := ""
	if loadErr != nil {
		warning = loadErr.Error()
	}
	host, _ := d.hostname()
	picker := client.NewNativePicker()
	token := client.NewToken()
	keys := client.NewKeyStore(d.lookup)
	if settingsPath != "" {
		if err := keys.EnableSaved(client.SavedKeyPath(settingsPath)); err != nil {
			warning = strings.TrimSpace(warning + " " + err.Error())
		}
	}
	server := client.NewUIServer(client.UIOptions{
		Settings: settings, SettingsWarn: warning, SettingsPath: settingsPath,
		Keys: keys, Session: session, Picker: picker, CanBrowse: client.CanBrowse(picker),
		Token: token, Source: client.DefaultSource(host), Version: version, Quit: quit,
	})
	return &ui{server: server, token: token}, nil
}
