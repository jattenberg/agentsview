package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesm/agentsview/internal/testjsonl"
)

func TestParseCursorSession(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantMsgCount int
		check        func(t *testing.T, sess *ParsedSession, msgs []ParsedMessage)
	}{
		{
			name: "basic session",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserJSON("Hello world"),
				testjsonl.CursorAssistantJSON("Hi there!"),
			),
			wantMsgCount: 2,
			check: func(t *testing.T, sess *ParsedSession, msgs []ParsedMessage) {
				assertSessionMeta(t, sess, "cursor:test-session", "proj", AgentCursor)
				assertMessage(t, msgs[0], RoleUser, "Hello world")
				assertMessage(t, msgs[1], RoleAssistant, "Hi there!")
				if msgs[0].Ordinal != 0 || msgs[1].Ordinal != 1 {
					t.Errorf("ordinals = %d,%d want 0,1",
						msgs[0].Ordinal, msgs[1].Ordinal)
				}
			},
		},
		{
			name: "first message extracts user_query",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserWithQueryJSON(
					"What does this code do?",
					"<system_reminder>be helpful</system_reminder>",
				),
				testjsonl.CursorAssistantJSON("It does X."),
			),
			wantMsgCount: 2,
			check: func(t *testing.T, sess *ParsedSession, _ []ParsedMessage) {
				if sess.FirstMessage != "What does this code do?" {
					t.Errorf("FirstMessage = %q, want %q",
						sess.FirstMessage,
						"What does this code do?")
				}
			},
		},
		{
			name: "full content stored including scaffolding",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserWithQueryJSON(
					"fix the bug",
					"<rules>some rules</rules>",
				),
			),
			wantMsgCount: 1,
			check: func(t *testing.T, _ *ParsedSession, msgs []ParsedMessage) {
				if !strings.Contains(msgs[0].Content, "<rules>") {
					t.Error("content should include scaffolding")
				}
				if !strings.Contains(msgs[0].Content, "fix the bug") {
					t.Error("content should include user query")
				}
				if msgs[0].ContentLength != len(msgs[0].Content) {
					t.Errorf("ContentLength = %d, want %d",
						msgs[0].ContentLength,
						len(msgs[0].Content))
				}
			},
		},
		{
			name: "first message fallback without user_query tag",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserJSON("plain question"),
				testjsonl.CursorAssistantJSON("answer"),
			),
			wantMsgCount: 2,
			check: func(t *testing.T, sess *ParsedSession, _ []ParsedMessage) {
				if sess.FirstMessage != "plain question" {
					t.Errorf("FirstMessage = %q, want %q",
						sess.FirstMessage, "plain question")
				}
			},
		},
		{
			name: "multiple consecutive assistant messages",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserJSON("go"),
				testjsonl.CursorAssistantJSON("step 1"),
				testjsonl.CursorAssistantJSON("step 2"),
				testjsonl.CursorAssistantJSON("step 3"),
			),
			wantMsgCount: 4,
			check: func(t *testing.T, _ *ParsedSession, msgs []ParsedMessage) {
				for i, want := range []RoleType{
					RoleUser, RoleAssistant,
					RoleAssistant, RoleAssistant,
				} {
					if msgs[i].Role != want {
						t.Errorf("msgs[%d].Role = %q, want %q",
							i, msgs[i].Role, want)
					}
					if msgs[i].Ordinal != i {
						t.Errorf("msgs[%d].Ordinal = %d, want %d",
							i, msgs[i].Ordinal, i)
					}
				}
			},
		},
		{
			name:         "empty file returns nil session",
			content:      "",
			wantMsgCount: -1, // sentinel: expect nil sess
		},
		{
			name: "single user message",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserJSON("only question"),
			),
			wantMsgCount: 1,
			check: func(t *testing.T, sess *ParsedSession, _ []ParsedMessage) {
				if sess.FirstMessage != "only question" {
					t.Errorf("FirstMessage = %q", sess.FirstMessage)
				}
			},
		},
		{
			name: "blank lines and whitespace-only messages skipped",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserJSON(""),
				testjsonl.CursorUserJSON("   "),
				testjsonl.CursorUserJSON("real message"),
			),
			wantMsgCount: 1,
			check: func(t *testing.T, _ *ParsedSession, msgs []ParsedMessage) {
				assertMessage(t, msgs[0], RoleUser, "real message")
			},
		},
		{
			name: "malformed JSON line skipped",
			content: testjsonl.CursorUserJSON("before") + "\n" +
				"not valid json\n" +
				testjsonl.CursorAssistantJSON("after") + "\n",
			wantMsgCount: 2,
			check: func(t *testing.T, _ *ParsedSession, msgs []ParsedMessage) {
				assertMessage(t, msgs[0], RoleUser, "before")
				assertMessage(t, msgs[1], RoleAssistant, "after")
			},
		},
		{
			name: "no timestamps in messages",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserJSON("hi"),
				testjsonl.CursorAssistantJSON("hello"),
			),
			wantMsgCount: 2,
			check: func(t *testing.T, sess *ParsedSession, msgs []ParsedMessage) {
				assertZeroTimestamp(t, sess.StartedAt, "StartedAt")
				for i, m := range msgs {
					assertZeroTimestamp(t, m.Timestamp,
						"msgs["+string(rune('0'+i))+"].Timestamp")
				}
				if sess.EndedAt.IsZero() {
					t.Error("EndedAt should be file mtime, not zero")
				}
			},
		},
		{
			name: "multiline user_query extraction",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserWithQueryJSON(
					"line 1\nline 2\nline 3",
					"<context>stuff</context>",
				),
			),
			wantMsgCount: 1,
			check: func(t *testing.T, sess *ParsedSession, _ []ParsedMessage) {
				if !strings.Contains(sess.FirstMessage, "line 1") {
					t.Errorf("FirstMessage missing line 1: %q",
						sess.FirstMessage)
				}
			},
		},
		{
			name: "unicode content",
			content: testjsonl.JoinJSONL(
				testjsonl.CursorUserJSON("cafe\u0301 emoji test"),
			),
			wantMsgCount: 1,
			check: func(t *testing.T, _ *ParsedSession, msgs []ParsedMessage) {
				if !strings.Contains(msgs[0].Content, "caf") {
					t.Error("unicode content not preserved")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTestFile(
				t, "test-session.jsonl", tt.content,
			)
			sess, msgs, err := ParseCursorSession(
				path, "proj", "local",
			)
			if err != nil {
				t.Fatalf("ParseCursorSession: %v", err)
			}

			if tt.wantMsgCount == -1 {
				if sess != nil {
					t.Fatal("expected nil session")
				}
				return
			}

			if sess == nil {
				t.Fatal("session is nil")
			}
			assertMessageCount(t, len(msgs), tt.wantMsgCount)
			if tt.check != nil {
				tt.check(t, sess, msgs)
			}
		})
	}
}

func TestParseCursorSessionSubagentMerge(t *testing.T) {
	dir := t.TempDir()

	parentID := "aaaa-bbbb-cccc"
	sessionDir := filepath.Join(
		dir, parentID,
	)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	parentContent := testjsonl.JoinJSONL(
		testjsonl.CursorUserJSON("parent question"),
		testjsonl.CursorAssistantJSON("parent answer"),
	)
	parentPath := filepath.Join(
		sessionDir, parentID+".jsonl",
	)
	if err := os.WriteFile(
		parentPath, []byte(parentContent), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	subID := "dddd-eeee-ffff"
	subContent := testjsonl.JoinJSONL(
		testjsonl.CursorUserJSON("sub task"),
		testjsonl.CursorAssistantJSON("sub result"),
	)
	subPath := filepath.Join(subDir, subID+".jsonl")
	if err := os.WriteFile(
		subPath, []byte(subContent), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	sess, msgs, err := ParseCursorSession(
		parentPath, "proj", "local",
	)
	if err != nil {
		t.Fatalf("ParseCursorSession: %v", err)
	}
	if sess == nil {
		t.Fatal("session is nil")
	}

	assertMessageCount(t, len(msgs), 4)

	assertMessage(t, msgs[0], RoleUser, "parent question")
	assertMessage(t, msgs[1], RoleAssistant, "parent answer")

	if !strings.Contains(msgs[2].Content, "[Subagent: "+subID+"]") {
		t.Errorf("subagent marker missing in msgs[2]: %q",
			msgs[2].Content[:80])
	}
	if !strings.Contains(msgs[2].Content, "sub task") {
		t.Errorf("subagent content missing in msgs[2]")
	}

	// Ordinals should be sequential
	for i, m := range msgs {
		if m.Ordinal != i {
			t.Errorf("msgs[%d].Ordinal = %d, want %d",
				i, m.Ordinal, i)
		}
	}

	if sess.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", sess.MessageCount)
	}
}

func TestParseCursorSessionNoSubagents(t *testing.T) {
	dir := t.TempDir()

	parentID := "xxxx-yyyy"
	sessionDir := filepath.Join(dir, parentID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := testjsonl.JoinJSONL(
		testjsonl.CursorUserJSON("question"),
		testjsonl.CursorAssistantJSON("answer"),
	)
	parentPath := filepath.Join(
		sessionDir, parentID+".jsonl",
	)
	if err := os.WriteFile(
		parentPath, []byte(content), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	sess, msgs, err := ParseCursorSession(
		parentPath, "proj", "local",
	)
	if err != nil {
		t.Fatalf("ParseCursorSession: %v", err)
	}
	if sess == nil {
		t.Fatal("session is nil")
	}
	assertMessageCount(t, len(msgs), 2)
}

func TestExtractCursorUserQuery(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "with user_query tag",
			text: "<context>stuff</context>\n<user_query>\nfix the bug\n</user_query>",
			want: "fix the bug",
		},
		{
			name: "no tag returns full text",
			text: "plain text question",
			want: "plain text question",
		},
		{
			name: "multiline query",
			text: "<user_query>\nline 1\nline 2\n</user_query>",
			want: "line 1\nline 2",
		},
		{
			name: "nested tags in query",
			text: "<user_query>\nquery with <code>tags</code> inside\n</user_query>",
			want: "query with <code>tags</code> inside",
		},
		{
			name: "empty user_query tag falls back",
			text: "<user_query>\n   \n</user_query>",
			want: "<user_query>\n   \n</user_query>",
		},
		{
			name: "empty string",
			text: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractCursorUserQuery(tt.text)
			if got != tt.want {
				t.Errorf("ExtractCursorUserQuery = %q, want %q",
					got, tt.want)
			}
		})
	}
}

func TestCursorProjectFromSlug(t *testing.T) {
	tests := []struct {
		name string
		slug string
		want string
	}{
		{
			name: "standard development path",
			slug: "Users-alice-development-agentsview",
			want: "agentsview",
		},
		{
			name: "empty slug",
			slug: "",
			want: "",
		},
		{
			name: "workspace JSON slug returns empty",
			slug: "Users-alice-Library-Application-Support-Cursor-Workspaces-1768716198560-workspace-json",
			want: "",
		},
		{
			name: "desktop path",
			slug: "Users-alice-Desktop-my-project",
			want: "project",
		},
		{
			name: "single component",
			slug: "myproject",
			want: "myproject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CursorProjectFromSlug(tt.slug)
			if got != tt.want {
				t.Errorf(
					"CursorProjectFromSlug(%q) = %q, want %q",
					tt.slug, got, tt.want)
			}
		})
	}
}
