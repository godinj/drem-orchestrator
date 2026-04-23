// csuite-inbox-watch monitors a C-Suite agent's inbox directory for new
// messages and notifies the agent's tmux session via send-keys.
//
// Usage:
//
//	csuite-inbox-watch -agent kyle [-socket drem] [-dir ~/.drem-csuite]
//
// The watcher uses fsnotify (inotify on Linux) to react to new files
// in $DIR/$AGENT/inbox/. When a .md file is created, it reads the YAML
// frontmatter (from, subject, priority) and injects a one-line notification
// into the agent's tmux session (csuite-$AGENT on the specified socket).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

func main() {
	agent := flag.String("agent", "", "Agent name (e.g., kyle, mike, alex, seth)")
	socket := flag.String("socket", "drem", "Tmux socket name")
	dir := flag.String("dir", "", "C-Suite base directory (default: ~/.drem-csuite)")
	flag.Parse()

	if *agent == "" {
		log.Fatal("required: -agent <name>")
	}

	if *dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("cannot determine home directory: %v", err)
		}
		*dir = filepath.Join(home, ".drem-csuite")
	}

	inboxDir := filepath.Join(*dir, *agent, "inbox")
	if _, err := os.Stat(inboxDir); os.IsNotExist(err) {
		log.Fatalf("inbox directory does not exist: %s", inboxDir)
	}

	session := "csuite-" + *agent

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("cannot create watcher: %v", err)
	}
	defer watcher.Close()

	if err := watcher.Add(inboxDir); err != nil {
		log.Fatalf("cannot watch %s: %v", inboxDir, err)
	}

	log.Printf("watching %s for agent %s (session %s on socket %s)", inboxDir, *agent, session, *socket)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) && strings.HasSuffix(event.Name, ".md") {
				from, subject, priority := readFrontmatter(event.Name)
				if from != "" {
					note := fmt.Sprintf("[inbox] %s from %s: %s", priority, from, subject)
					notify(session, *socket, note)
					log.Printf("notified: %s", note)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

// readFrontmatter extracts from, subject, and priority from YAML frontmatter.
func readFrontmatter(path string) (from, subject, priority string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inFrontmatter := false

	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			if inFrontmatter {
				break // end of frontmatter
			}
			inFrontmatter = true
			continue
		}
		if !inFrontmatter {
			continue
		}
		if k, v, ok := parseYAMLField(line); ok {
			switch k {
			case "from":
				from = v
			case "subject":
				subject = v
			case "priority":
				priority = v
			}
		}
	}

	if priority == "" {
		priority = "normal"
	}
	return from, subject, priority
}

// parseYAMLField extracts a key-value pair from a simple YAML line.
func parseYAMLField(line string) (key, value string, ok bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key = strings.TrimSpace(parts[0])
	value = strings.TrimSpace(parts[1])
	// Strip surrounding quotes
	value = strings.Trim(value, "\"'")
	return key, value, true
}

// notify sends a tmux send-keys command to the agent's session.
func notify(session, socket, note string) {
	cmd := exec.Command("tmux", "-L", socket, "send-keys", "-t", session, note, "Enter")
	if err := cmd.Run(); err != nil {
		log.Printf("tmux send-keys failed (session %s may not be running): %v", session, err)
	}
}
