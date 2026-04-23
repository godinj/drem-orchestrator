package deliver

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// frontmatterCap is the maximum byte count read from an outbox file
// when parsing frontmatter. The plan notes messages should be well
// under 64 KiB — a persona reply is typically a few hundred bytes —
// but the cap prevents a pathological file (accidental binary dump,
// runaway log splice) from consuming unbounded memory.
const frontmatterCap = 64 * 1024

// Destination class names. Kept as typed string constants so callers
// don't sprinkle literals through switch statements.
const (
	ClassPersona    = "persona"
	ClassKyle       = "kyle"
	ClassQuarantine = "quarantine"
)

// Classification is the result of reading and classifying an outbox
// file's frontmatter. Dest names the persona or "kyle" for real
// classes; it is empty for the quarantine class.
type Classification struct {
	Class  string // ClassPersona | ClassKyle | ClassQuarantine
	Dest   string // destination persona (for ClassPersona), "kyle" (for ClassKyle), empty (for ClassQuarantine)
	Reason string // diagnostic reason when Class == ClassQuarantine
}

// ErrMultiRecipient signals that the frontmatter's "to:" value is a
// YAML sequence rather than a scalar. The plan explicitly rejects
// multi-recipient messages (§5 Q4); callers translate this to
// HTTP 400.
var ErrMultiRecipient = errors.New("multi-recipient not supported")

// ClassifyFile opens the source outbox file, reads up to
// frontmatterCap bytes, parses its YAML frontmatter, and classifies
// the destination. A nil error indicates classification succeeded —
// including the quarantine class, which is a routing decision, not a
// failure. Multi-recipient "to:" values return ErrMultiRecipient.
//
// Callers pass the protocol path (the DeliverRequest's outbox_path
// field) which starts with /csuite/. The package-level csuiteRoot
// var remaps that to the on-disk location; tests point it at a
// tempdir, production leaves it at the compose-mounted /csuite.
// The file is opened read-only.
func ClassifyFile(path string) (Classification, error) {
	realPath := resolveCsuitePath(path)
	f, err := os.Open(realPath) //nolint:gosec // path validated upstream by ValidateRequest
	if err != nil {
		return Classification{}, fmt.Errorf("open %s: %w", realPath, err)
	}
	defer f.Close()

	buf := make([]byte, frontmatterCap)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Classification{}, fmt.Errorf("read %s: %w", realPath, err)
	}
	return classifyBytes(buf[:n])
}

// classifyBytes is the pure classification kernel extracted for test
// coverage without hitting disk.
func classifyBytes(data []byte) (Classification, error) {
	body, ok := extractFrontmatter(data)
	if !ok {
		return Classification{Class: ClassQuarantine, Reason: "no frontmatter delimiters"}, nil
	}

	// Decode the frontmatter with a permissive node type so we can
	// distinguish scalar (string) from sequence (list) without
	// committing to a specific struct shape. The plan tolerates
	// unknown keys — only "to:" matters for routing.
	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil {
		return Classification{Class: ClassQuarantine, Reason: "unparseable frontmatter: " + err.Error()}, nil
	}

	toNode, found := findToNode(&root)
	if !found {
		return Classification{Class: ClassQuarantine, Reason: "missing 'to' field"}, nil
	}

	switch toNode.Kind {
	case yaml.SequenceNode:
		return Classification{}, ErrMultiRecipient
	case yaml.ScalarNode:
		dest := toNode.Value
		switch dest {
		case "mike", "alex", "seth":
			return Classification{Class: ClassPersona, Dest: dest}, nil
		case "kyle":
			return Classification{Class: ClassKyle, Dest: "kyle"}, nil
		case "":
			return Classification{Class: ClassQuarantine, Reason: "empty 'to' field"}, nil
		default:
			return Classification{Class: ClassQuarantine, Reason: "unknown recipient " + dest}, nil
		}
	default:
		// MappingNode, AliasNode, etc. are not meaningful for a
		// routing address — quarantine.
		return Classification{Class: ClassQuarantine, Reason: "unsupported 'to' node kind"}, nil
	}
}

// frontmatterOpen / frontmatterClose are the conventional Jekyll-style
// YAML frontmatter delimiters used throughout the csuite outbox
// format.
var (
	frontmatterOpen  = []byte("---\n")
	frontmatterClose = []byte("\n---")
)

// extractFrontmatter returns the bytes between the leading "---\n"
// and the next "\n---" delimiter. The second return is false if the
// input has no valid frontmatter block.
func extractFrontmatter(data []byte) ([]byte, bool) {
	if !bytes.HasPrefix(data, frontmatterOpen) {
		return nil, false
	}
	rest := data[len(frontmatterOpen):]
	end := bytes.Index(rest, frontmatterClose)
	if end < 0 {
		return nil, false
	}
	return rest[:end], true
}

// findToNode walks the root yaml.Node tree looking for a
// top-level "to:" key. Returns the value node and true on success.
// Case-sensitive — the csuite message format pins lowercase field
// names.
func findToNode(root *yaml.Node) (*yaml.Node, bool) {
	if root == nil {
		return nil, false
	}
	// yaml.Unmarshal produces a DocumentNode wrapping the mapping.
	var doc *yaml.Node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		doc = root.Content[0]
	} else {
		doc = root
	}
	if doc == nil || doc.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		val := doc.Content[i+1]
		if key.Kind == yaml.ScalarNode && key.Value == "to" {
			return val, true
		}
	}
	return nil, false
}
