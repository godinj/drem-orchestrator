package agent

// This file previously contained the SessionManager interface for tmux-based
// agent execution. With the migration to subprocess-based agents, tmux is
// only used for supervisors and shells, which interact directly with
// *tmux.Manager via Runner.tmuxMgr.
