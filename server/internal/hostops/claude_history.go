package hostops

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// The conversations themselves are the CLI's, not HostMan's. A HostMan
// session is a process that is let go after an hour alone and dies with a
// restart, but the conversation it carried is written by the CLI to the
// user's home on the host — one file per session under
// ~/.claude/projects/<the working directory, with every slash a dash>/ —
// and `claude --resume` reads it back. So "my session is gone" is almost
// never true: the process is gone, and the conversation is sitting on the
// host waiting to be picked up. This file is how HostMan finds them.
//
// The files are the CLI's own format, one JSON object per line, and nothing
// here depends on more of it than a name and a place: the working directory
// is on every message line, the title is a line of its own when the CLI has
// made one, and the first thing the user said is there failing that. Only
// the head and the tail of each file come across — a long conversation runs
// to megabytes, and the title lines the CLI appends are at the end — so a
// listing of fifty conversations costs one SSH round trip and a few hundred
// kilobytes, however long the conversations were.

const (
	// MaxClaudeHistory is how many conversations a listing returns, newest
	// first. It is a screen's worth and then some; older ones are still on
	// the host, and `claude --resume` there lists them all.
	MaxClaudeHistory = 50

	// claudeHistoryHead and claudeHistoryTail are how much of each file is
	// read: the head for the first message and the working directory, the
	// tail for the title lines the CLI writes as the conversation goes on.
	claudeHistoryHead = 12 << 10
	claudeHistoryTail = 4 << 10
)

// ClaudeConversation is one conversation the CLI has on file for the SSH
// user: enough to recognise it and the id to resume it with.
type ClaudeConversation struct {
	// ID is the CLI's session id, the one `claude --resume` takes.
	ID string `json:"id"`
	// Name is what to call it: the title the CLI gave it, or the first thing
	// the user said.
	Name string `json:"name"`
	// Dir is the working directory the conversation ran in.
	Dir string `json:"dir"`
	// Branch is the git branch the directory was on, when there was one.
	Branch string `json:"branch,omitempty"`
	// StartedAt is the first message's time, when the head of the file had
	// one; ModifiedAt is when the file was last written, which is the last
	// time anything was said.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	ModifiedAt time.Time  `json:"modifiedAt"`
	// Size is the file's, in bytes: a rough measure of how much was said.
	Size int64 `json:"size"`
}

// claudeHistoryScript lists the newest conversation files and prints the
// head and the tail of each, between markers the parser reads. It reads
// and never writes, and a host with no conversations prints nothing.
//
// Subagent transcripts live a directory deeper and are not conversations a
// person can resume, which the depth limit keeps out.
const claudeHistoryScript = `set -u
max=%d
head_bytes=%d
tail_bytes=%d
root="$HOME/.claude/projects"
[ -d "$root" ] || exit 0
find "$root" -mindepth 2 -maxdepth 2 -type f -name '*.jsonl' 2>/dev/null |
while IFS= read -r f; do
  stat -c '%%Y %%s %%n' "$f" 2>/dev/null || stat -f '%%m %%z %%N' "$f" 2>/dev/null
done | sort -rn | head -n "$max" |
while read -r mtime size f; do
  [ "$size" -gt 0 ] 2>/dev/null || continue
  printf '@@file\t%%s\t%%s\t%%s\n' "$mtime" "$size" "$f"
  head -c "$head_bytes" "$f" 2>/dev/null
  printf '\n@@tail\n'
  if [ "$size" -gt "$head_bytes" ]; then
    tail -c "$tail_bytes" "$f" 2>/dev/null
  fi
  printf '\n'
done
printf '@@end\n'
exit 0
`

// ClaudeConversations lists the conversations the CLI has on file for the
// SSH user on a host, newest first.
func (s *Service) ClaudeConversations(ctx context.Context, h *store.Host) ([]ClaudeConversation, error) {
	script := fmt.Sprintf(claudeHistoryScript, MaxClaudeHistory, claudeHistoryHead, claudeHistoryTail)
	res, err := s.run(ctx, h, asUser(script), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not list Claude's conversations on "+h.Name)
	}
	return parseClaudeHistory(res.Stdout), nil
}

// historyLine is one line of a conversation file, with only the parts a
// listing reads. The CLI writes many kinds of line; the ones that matter
// are the user's messages, which carry the directory and the time, and the
// title lines, which carry a name.
type historyLine struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	Timestamp string `json:"timestamp"`
	IsMeta    bool   `json:"isMeta"`
	// A sidechain is a subagent's line that landed in the main file.
	IsSidechain bool `json:"isSidechain"`
	Message     *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	// The names, by how much a person meant them: a title they typed, a
	// title the CLI made, the CLI's summary of the conversation, and the
	// last thing they said.
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	Summary     string `json:"summary"`
	LastPrompt  string `json:"lastPrompt"`
}

// parseClaudeHistory turns the script's output into conversations, newest
// first, as the script listed them.
func parseClaudeHistory(out string) []ClaudeConversation {
	var found []ClaudeConversation
	var cur *ClaudeConversation
	var lines []string
	flush := func() {
		if cur == nil {
			return
		}
		if c, ok := readConversation(*cur, lines); ok {
			found = append(found, c)
		}
		cur, lines = nil, nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "@@file\t"):
			flush()
			parts := strings.SplitN(line, "\t", 4)
			if len(parts) != 4 {
				continue
			}
			mtime, _ := strconv.ParseInt(parts[1], 10, 64)
			size, _ := strconv.ParseInt(parts[2], 10, 64)
			cur = &ClaudeConversation{
				ID:         strings.TrimSuffix(path.Base(parts[3]), ".jsonl"),
				ModifiedAt: time.Unix(mtime, 0).UTC(),
				Size:       size,
			}
		case line == "@@tail", line == "@@end":
			// The tail's first line may be the end of one cut in half; a
			// line that does not parse is skipped, which is what happens to
			// it. Nothing else to do here.
		default:
			if cur != nil && line != "" {
				lines = append(lines, line)
			}
		}
	}
	flush()
	return found
}

// readConversation fills in a conversation from the lines of its file that
// came across. It reports false for a file with nothing in it a person
// would recognise: no message and no title, which a session that was opened
// and abandoned leaves behind.
func readConversation(c ClaudeConversation, lines []string) (ClaudeConversation, bool) {
	var custom, ai, summary, firstSaid, lastPrompt string
	spoke := false
	for _, raw := range lines {
		var l historyLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}
		if l.IsSidechain {
			continue
		}
		if c.Dir == "" && l.Cwd != "" {
			c.Dir = l.Cwd
		}
		if c.Branch == "" && l.GitBranch != "" {
			c.Branch = l.GitBranch
		}
		switch l.Type {
		case "user":
			if c.StartedAt == nil {
				if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
					t = t.UTC()
					c.StartedAt = &t
				}
			}
			if l.IsMeta || l.Message == nil || l.Message.Role != "user" {
				continue
			}
			if text := spokenText(l.Message.Content); text != "" {
				spoke = true
				if firstSaid == "" {
					firstSaid = text
				}
			}
		case "custom-title":
			custom = l.CustomTitle
		case "ai-title":
			ai = l.AITitle
		case "summary":
			summary = l.Summary
		case "last-prompt":
			lastPrompt = l.LastPrompt
		}
	}
	for _, name := range []string{custom, ai, summary, firstSaid, lastPrompt} {
		if name = title(name); name != "" {
			c.Name = name
			break
		}
	}
	if c.Name == "" && !spoke {
		return c, false
	}
	return c, true
}

// spokenText is what the user typed in a message, or "" when the message is
// the machinery's: a tool result, a slash command's expansion, a system
// reminder. Content is a string or a list of blocks.
func spokenText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return spoken(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" {
			if t := spoken(b.Text); t != "" {
				return t
			}
		}
	}
	return ""
}

func spoken(s string) string {
	s = strings.TrimSpace(s)
	// The CLI records what it did on the user's behalf as user messages
	// wrapped in tags; a person's words do not start with one.
	if strings.HasPrefix(s, "<") {
		return ""
	}
	return s
}

// title is a name fit for a row: one line, not too long.
func title(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 80 {
		cut := 80
		for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}
