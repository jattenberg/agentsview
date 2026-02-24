package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

var userQueryRe = regexp.MustCompile(
	`(?s)<user_query>\s*(.*?)\s*</user_query>`,
)

// ParseCursorSession parses a Cursor agent transcript JSONL
// file and merges any subagent transcripts from the same
// session directory into a single unified session.
func ParseCursorSession(
	path, project, machine string,
) (*ParsedSession, []ParsedMessage, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}

	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")

	messages, err := parseCursorJSONL(path)
	if err != nil {
		return nil, nil, err
	}

	subagentMsgs := mergeCursorSubagents(
		filepath.Dir(path), len(messages),
	)
	messages = append(messages, subagentMsgs...)

	if len(messages) == 0 {
		return nil, nil, nil
	}

	firstMsg := extractFirstCursorQuery(messages)

	sess := &ParsedSession{
		ID:           "cursor:" + sessionID,
		Project:      project,
		Machine:      machine,
		Agent:        AgentCursor,
		FirstMessage: firstMsg,
		EndedAt:      info.ModTime(),
		MessageCount: len(messages),
		File: FileInfo{
			Path:  path,
			Size:  info.Size(),
			Mtime: info.ModTime().UnixNano(),
		},
	}

	return sess, messages, nil
}

// parseCursorJSONL reads a Cursor transcript JSONL file and
// returns parsed messages with ordinals starting at startOrd.
func parseCursorJSONL(path string) ([]ParsedMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var (
		messages []ParsedMessage
		ordinal  int
	)

	lr := newLineReader(f, maxLineSize)

	for {
		line, ok := lr.next()
		if !ok {
			break
		}

		if !gjson.Valid(line) {
			continue
		}

		role := gjson.Get(line, "role").Str
		if role != "user" && role != "assistant" {
			continue
		}

		content := gjson.Get(line, "message.content")
		text := extractCursorText(content)
		if strings.TrimSpace(text) == "" {
			continue
		}

		messages = append(messages, ParsedMessage{
			Ordinal:       ordinal,
			Role:          RoleType(role),
			Content:       text,
			ContentLength: len(text),
		})
		ordinal++
	}

	if err := lr.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return messages, nil
}

// extractCursorText concatenates all text blocks from a Cursor
// message content array.
func extractCursorText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.Str
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").Str == "text" {
			if t := block.Get("text").Str; t != "" {
				parts = append(parts, t)
			}
		}
		return true
	})
	return strings.Join(parts, "\n")
}

// mergeCursorSubagents discovers and parses all subagent JSONL
// files in the session directory, returning their messages with
// ordinals continuing from startOrd and content prefixed with a
// subagent marker.
func mergeCursorSubagents(
	sessionDir string, startOrd int,
) []ParsedMessage {
	subDir := filepath.Join(sessionDir, "subagents")
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return nil
	}

	var all []ParsedMessage
	ordinal := startOrd

	for _, entry := range entries {
		if entry.IsDir() ||
			!strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		subID := strings.TrimSuffix(entry.Name(), ".jsonl")
		subPath := filepath.Join(subDir, entry.Name())

		msgs, err := parseCursorJSONL(subPath)
		if err != nil {
			continue
		}

		prefix := fmt.Sprintf("[Subagent: %s]\n", subID)
		for _, m := range msgs {
			m.Ordinal = ordinal
			m.Content = prefix + m.Content
			m.ContentLength = len(m.Content)
			ordinal++
			all = append(all, m)
		}
	}
	return all
}

// extractFirstCursorQuery finds the first user message and
// extracts the <user_query> content for use as FirstMessage.
func extractFirstCursorQuery(
	messages []ParsedMessage,
) string {
	for _, m := range messages {
		if m.Role != RoleUser {
			continue
		}
		query := ExtractCursorUserQuery(m.Content)
		return truncate(
			strings.ReplaceAll(query, "\n", " "), 300,
		)
	}
	return ""
}

// ExtractCursorUserQuery extracts the content within
// <user_query>...</user_query> tags. Falls back to the full
// text if no tag is found.
func ExtractCursorUserQuery(text string) string {
	match := userQueryRe.FindStringSubmatch(text)
	if len(match) >= 2 && strings.TrimSpace(match[1]) != "" {
		return strings.TrimSpace(match[1])
	}
	return text
}

// CursorProjectFromSlug converts a Cursor workspace directory
// slug back to a path and extracts a project name from it.
// Slugs encode paths with hyphens replacing separators:
// Users-alice-code-my-app -> /Users/alice/code/my-app.
//
// The decoding is inherently lossy since both path separators
// and literal hyphens become hyphens in the slug. We use
// heuristics to produce a reasonable project name.
func CursorProjectFromSlug(slug string) string {
	if slug == "" {
		return ""
	}

	// Workspace-JSON slugs don't represent real project paths.
	if strings.Contains(slug, "Application-Support-Cursor-Workspaces") {
		return ""
	}

	// Attempt to reconstruct the path by replacing hyphens
	// with path separators. This is lossy but sufficient for
	// project extraction since ExtractProjectFromCwd looks
	// for git roots and known project parent markers.
	reconstructed := "/" + strings.ReplaceAll(slug, "-", "/")

	if project := ExtractProjectFromCwd(reconstructed); project != "" {
		return project
	}

	// Fallback: use the last slug component.
	parts := strings.Split(slug, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return normalizeName(parts[i])
		}
	}
	return ""
}
