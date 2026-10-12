package ghost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aaronjauregui/haunt/internal/model"
)

// The transcript is the conversation the model sees: every turn's input,
// the calls it made and what they returned, and what it said or that it
// said nothing. It lives in ghosts/<name>/transcript.db, a file of its own
// beside state.db, written only by the running ghost and read by `haunt
// history` and `haunt journal -echoes` with the ghost still running (WAL).
//
// One row is one chat message, so replaying a turn is reading its rows in
// order: an assistant message carrying tool calls is one row holding them
// all, and each result is a row of its own. Three kinds of row are markers
// with no message of their own: how a turn ended when it ended in silence
// or failure.
//
// Compaction is the driver's. When the rows the model would see pass a
// share of the model's context (compactShare), the older turns are
// summarised by a call with thinking off into one summary row, and the
// last compactKeep turns stay verbatim. The summarised rows are kept,
// marked compacted, for history and for recall; the prompt reads only
// what is not compacted. Errands never ride on the transcript (they come
// from the store on every turn's context), so a summary cannot lose one.
//
// A second FTS5 table indexes the person's words, the ghost's and the
// summaries, so recall can search the conversation as well as memory.

// Row kinds.
const (
	rowInput   = "input"   // the person's words
	rowEvent   = "event"   // an envelope
	rowCalls   = "calls"   // an assistant message with tool calls; Args is the calls as JSON
	rowResult  = "result"  // one tool call's result
	rowWords   = "words"   // what the ghost said, ending the turn
	rowSilent  = "silent"  // marker: the turn ended in silence
	rowFailed  = "failed"  // marker: the turn failed
	rowSummary = "summary" // the summary of compacted turns
)

const (
	// compactShare is how much of the model's context the transcript may
	// take before the older turns are summarised. The rest is the
	// standing prompt, memory, skills, this turn's tool rounds and the
	// answer.
	compactShare = 0.6
	// compactKeep is how many of the latest turns stay verbatim.
	compactKeep = 12
	// defaultContext is the context assumed when [model] context_length
	// is not set.
	defaultContext = 32768
)

// TranscriptRow is one row of a ghost's transcript, as `haunt history`
// reads it.
type TranscriptRow struct {
	ID   int64
	Turn int64
	Kind string
	// Text is the words of an input, an event, words, a summary, or a
	// result; for calls, any words written beside the calls.
	Text string
	// Tool and CallID name a result's call.
	Tool, CallID string
	// Calls are a calls row's tool calls.
	Calls []TranscriptCall
	// Pose is the face worn with words.
	Pose      string
	At        time.Time
	Compacted bool
}

// TranscriptCall is one tool call in a calls row.
type TranscriptCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Ends reports whether the row ends its turn.
func (r TranscriptRow) Ends() bool {
	return r.Kind == rowWords || r.Kind == rowSilent || r.Kind == rowFailed
}

// transcript is the ghost's handle on its transcript.db.
type transcript struct {
	db *sql.DB
	mu sync.Mutex
}

const transcriptSchema = `
	PRAGMA journal_mode = WAL;
	CREATE TABLE IF NOT EXISTS rows (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		turn      INTEGER NOT NULL,
		kind      TEXT NOT NULL,
		text      TEXT NOT NULL DEFAULT '',
		tool      TEXT NOT NULL DEFAULT '',
		call_id   TEXT NOT NULL DEFAULT '',
		args      TEXT NOT NULL DEFAULT '',
		pose      TEXT NOT NULL DEFAULT '',
		at        TEXT NOT NULL,
		compacted INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS rows_turn ON rows(turn);
	CREATE VIRTUAL TABLE IF NOT EXISTS transcript_fts USING fts5(text, kind UNINDEXED, turn UNINDEXED, at UNINDEXED);
`

// openTranscript opens or creates the transcript at path.
func openTranscript(path string) (*transcript, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(transcriptSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening the transcript: %w", err)
	}
	return &transcript{db: db}, nil
}

func (t *transcript) Close() error { return t.db.Close() }

// nextTurn is the number the next turn takes.
func (t *transcript) nextTurn() (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var n sql.NullInt64
	if err := t.db.QueryRow(`SELECT MAX(turn) FROM rows`).Scan(&n); err != nil {
		return 0, err
	}
	return n.Int64 + 1, nil
}

// add appends one row.
func (t *transcript) add(r TranscriptRow) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r.At.IsZero() {
		r.At = time.Now()
	}
	args := ""
	if len(r.Calls) > 0 {
		b, err := json.Marshal(r.Calls)
		if err != nil {
			return err
		}
		args = string(b)
	}
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := r.At.Format(time.RFC3339Nano)
	res, err := tx.Exec(`INSERT INTO rows (turn, kind, text, tool, call_id, args, pose, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Turn, r.Kind, r.Text, r.Tool, r.CallID, args, r.Pose, at)
	if err != nil {
		return err
	}
	if searchable(r.Kind) && strings.TrimSpace(r.Text) != "" {
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO transcript_fts (rowid, text, kind, turn, at) VALUES (?, ?, ?, ?, ?)`,
			id, r.Text, r.Kind, r.Turn, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// searchable is the rows recall searches: what the person said, what the
// ghost said, and the summaries of both. Tool results are left out: a
// page found again through recall would be somebody else's words arriving
// with nothing to say so.
func searchable(kind string) bool {
	return kind == rowInput || kind == rowWords || kind == rowSummary
}

// live is the rows the model sees: everything not compacted, oldest first.
func (t *transcript) live() ([]TranscriptRow, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return queryRows(t.db, `WHERE compacted = 0 ORDER BY turn, id`)
}

// lastExchange is when the person or the ghost last said something: the
// at of the newest input or words row, compacted or not, since a summary
// does not make a conversation more recent. Events and silences are not
// exchanges. False before the first, or when the file cannot be read.
func (t *transcript) lastExchange() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var at string
	err := t.db.QueryRow(`SELECT at FROM rows WHERE kind IN (?, ?) ORDER BY id DESC LIMIT 1`, rowInput, rowWords).Scan(&at)
	if err != nil {
		return time.Time{}, false
	}
	when, err := time.Parse(time.RFC3339Nano, at)
	return when, err == nil
}

// TranscriptHit is one line of the conversation recall found.
type TranscriptHit struct {
	Turn int64
	Kind string
	Text string
	At   time.Time
}

// search is BM25 over the searchable rows, best first.
func (t *transcript) search(ctx context.Context, q string, n int) ([]TranscriptHit, error) {
	match := transcriptQuery(q)
	if match == "" {
		return nil, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	rows, err := t.db.QueryContext(ctx, `SELECT text, kind, turn, at FROM transcript_fts WHERE transcript_fts MATCH ?
		ORDER BY bm25(transcript_fts) LIMIT ?`, match, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptHit
	for rows.Next() {
		var h TranscriptHit
		var at string
		if err := rows.Scan(&h.Text, &h.Kind, &h.Turn, &at); err != nil {
			return nil, err
		}
		h.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, h)
	}
	return out, rows.Err()
}

// transcriptQuery makes a query safe for MATCH: its words, each quoted,
// any of them.
func transcriptQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " OR ")
}

// queryRows reads rows with a trailing clause.
func queryRows(db *sql.DB, clause string, args ...any) ([]TranscriptRow, error) {
	rows, err := db.Query(`SELECT id, turn, kind, text, tool, call_id, args, pose, at, compacted FROM rows `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptRow
	for rows.Next() {
		var r TranscriptRow
		var args, at string
		var compacted int
		if err := rows.Scan(&r.ID, &r.Turn, &r.Kind, &r.Text, &r.Tool, &r.CallID, &args, &r.Pose, &at, &compacted); err != nil {
			return nil, err
		}
		if args != "" {
			json.Unmarshal([]byte(args), &r.Calls)
		}
		r.At, _ = time.Parse(time.RFC3339Nano, at)
		r.Compacted = compacted != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReadTranscript reads a ghost's whole transcript, oldest first, without
// writing to it: the ghost may be running, and it is the one writer. A
// transcript that does not exist yet is empty.
func ReadTranscript(path string) ([]TranscriptRow, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := queryRows(db, `ORDER BY turn, id`)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return nil, nil
	}
	if err != nil && strings.Contains(err.Error(), "unable to open") {
		return nil, nil
	}
	return rows, err
}

// replay renders the live rows as the messages of part 4 of the prompt:
// the summary, then every finished turn. A turn that failed or never
// finished is left out whole, input and all: the model never answered it,
// and a question with no answer after it reads as one still open.
func replay(rows []TranscriptRow) []model.Message {
	var out []model.Message
	ended := map[int64]string{}
	for _, r := range rows {
		if r.Ends() {
			ended[r.Turn] = r.Kind
		}
	}
	for _, r := range rows {
		if r.Kind == rowSummary {
			out = append(out, model.Message{Role: "system", Content: "Earlier, in summary:\n" + r.Text})
			continue
		}
		if end := ended[r.Turn]; end == "" || end == rowFailed {
			continue
		}
		switch r.Kind {
		case rowInput, rowEvent:
			out = append(out, model.Message{Role: "user", Content: r.Text})
		case rowCalls:
			m := model.Message{Role: "assistant", Content: r.Text}
			for _, c := range r.Calls {
				m.ToolCalls = append(m.ToolCalls, model.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
			}
			out = append(out, m)
		case rowResult:
			out = append(out, model.Message{Role: "tool", Content: r.Text, ToolCallID: r.CallID, Name: r.Tool})
		case rowWords:
			out = append(out, model.Message{Role: "assistant", Content: r.Text})
		}
	}
	return out
}

// estimateTokens is a rough count of what rows cost in context: four
// bytes a token, and a few for each message's framing. Rough on purpose:
// it decides when to summarise, and a summary a little early or late
// costs nothing.
func estimateTokens(msgs []model.Message) int {
	n := 0
	for _, m := range msgs {
		n += 4 + len(m.Content)/4
		for _, c := range m.ToolCalls {
			n += 4 + (len(c.Name)+len(c.Args))/4
		}
	}
	return n
}

// compactBudget is the share of the context the transcript may take.
func compactBudget(contextLength int) int {
	if contextLength <= 0 {
		contextLength = defaultContext
	}
	return int(float64(contextLength) * compactShare)
}

// compactPrompt is what the summary call is told. It is the driver
// asking, in the second person, about the ghost's own conversation.
const compactPrompt = "You are summarising your own earlier conversation with the person you live with, " +
	"so you can carry it forward in fewer words. Write a short plain summary in the first person: " +
	"what they told you about themselves, what you agreed or took on, what you were talking about, " +
	"and anything you said you would do. Keep names, dates and numbers exactly. " +
	"Leave out what you looked up unless they asked you to keep it. No preamble."

// compact summarises the older turns when the live rows pass the budget,
// keeping the last compactKeep turns verbatim. summarise makes the call;
// it is given the earlier summary and the turns as text.
func (t *transcript) compact(ctx context.Context, budget int, summarise func(ctx context.Context, text string) (string, error)) (bool, error) {
	rows, err := t.live()
	if err != nil {
		return false, err
	}
	if estimateTokens(replay(rows)) <= budget {
		return false, nil
	}
	var turns []int64
	seen := map[int64]bool{}
	for _, r := range rows {
		if r.Kind != rowSummary && !seen[r.Turn] {
			seen[r.Turn] = true
			turns = append(turns, r.Turn)
		}
	}
	if len(turns) <= compactKeep {
		return false, nil
	}
	cut := turns[len(turns)-compactKeep] // the first turn kept
	var b strings.Builder
	var last int64
	for _, r := range rows {
		if r.Kind != rowSummary && r.Turn >= cut {
			continue
		}
		if r.Turn > last {
			last = r.Turn
		}
		if line := digestLine(r); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	summary, err := summarise(ctx, b.String())
	if err != nil {
		return false, err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false, errors.New("the summary came back empty")
	}
	t.mu.Lock()
	tx, err := t.db.Begin()
	if err != nil {
		t.mu.Unlock()
		return false, err
	}
	defer func() {
		tx.Rollback()
		t.mu.Unlock()
	}()
	if _, err := tx.Exec(`UPDATE rows SET compacted = 1 WHERE compacted = 0 AND (turn < ? OR kind = ?)`, cut, rowSummary); err != nil {
		return false, err
	}
	at := time.Now().Format(time.RFC3339Nano)
	res, err := tx.Exec(`INSERT INTO rows (turn, kind, text, at) VALUES (?, ?, ?, ?)`, last, rowSummary, summary, at)
	if err != nil {
		return false, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO transcript_fts (rowid, text, kind, turn, at) VALUES (?, ?, ?, ?, ?)`,
		id, summary, rowSummary, last, at); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// digestLine is one row as the summary call reads it.
func digestLine(r TranscriptRow) string {
	switch r.Kind {
	case rowSummary:
		return "(summary so far) " + r.Text
	case rowInput:
		return "them: " + r.Text
	case rowEvent:
		return "event: " + strings.TrimSpace(strings.TrimPrefix(r.Text, EnvelopePrefix))
	case rowWords:
		return "you: " + r.Text
	case rowCalls:
		var names []string
		for _, c := range r.Calls {
			if c.Name != "speak" && c.Name != "stay_silent" {
				names = append(names, c.Name+" "+clip(string(c.Args), 200))
			}
		}
		if len(names) == 0 {
			return ""
		}
		return "you used " + strings.Join(names, "; ")
	case rowResult:
		if r.Tool == "speak" || r.Tool == "stay_silent" {
			return ""
		}
		return "  → " + clip(r.Text, 300)
	case rowSilent:
		return "you said nothing"
	}
	return ""
}
