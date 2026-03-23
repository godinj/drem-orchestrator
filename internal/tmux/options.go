package tmux

// Option configures a Manager. Use WithSocket and WithConfigFile to set
// tmux global flags that isolate the server or load a custom config.
type Option func(*Manager)

// WithSocket returns an Option that sets the tmux socket name (-L flag).
// When set, every tmux invocation uses this dedicated server socket,
// fully isolating sessions from the user's default tmux server.
func WithSocket(name string) Option {
	return func(m *Manager) {
		m.Socket = name
	}
}

// WithConfigFile returns an Option that sets the tmux config file (-f flag).
// When set, every tmux invocation loads this config instead of ~/.tmux.conf.
func WithConfigFile(path string) Option {
	return func(m *Manager) {
		m.ConfigFile = path
	}
}
