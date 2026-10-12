package ghost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
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
// all, and each result is a row of its own. Some rows are markers with no
// message of their own: how a turn ended when it ended in silence or
// failure, and the opening words the person gave the reply (a lead row),
// which is never replayed, summarised or searched, so the model is not
// told it was led and recall does not find the lead as anybody's words.
//
// Compaction is the driver's. When the rows the model would see pass a
// share of the model's context (compactShare), whole days go first: the
// oldest day before today that has a diary entry (diary.go) is folded
// behind it, its rows marked compacted and one diary row put in their
// place, and so on until the rows fit or only today is left. A day is
// never summarised again once it has an entry; one that could not be
// given an entry is summarised on its own, into a summary row on that
// day. Only when today alone is over the share are its older turns
// summarised by a call with thinking off into one summary row, with the
// last compactKeep turns verbatim, and past days' own summaries rolled in
// with them; a day behind its entry never is. The
// compacted rows are kept for history and for recall; the prompt reads
// only what is not compacted. Errands never ride on the transcript (they
// come from the store on every turn's context), so a summary cannot lose
// one.
//
// A day is the local calendar day its turn began on. The ghost's own
// reflection turns (kind=reflect, reflect.go) belong to no day and are
// compacted as they end: they are about a day, and what they wrote is in
// memory. A folded day's diary row is for history and recall; the prompt
// carries the diary in part 2 instead.
//
// A second FTS5 table indexes the person's words, the ghost's, the
// summaries and the diary rows, so recall can search the conversation as
// well as memory.

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
	rowDiary   = "diary"   // a day folded behind its diary entry; At is on that day
	// rowLead is a marker: the opening words the person put in the
	// ghost's mouth for the turn's reply (turn.lead), in Text, with how
	// they were given in Tool (leadPrefilled, leadHinted). Written just
	// before the turn's words or silence.
	rowLead = "lead"
)

// How a lead was given, on its row: as the speak call's prefill, which
// opens the words, or only as the hint in the turn's context, when the
// words were the decide call's own text and may not open with it.
const (
	leadPrefilled = "prefill"
	leadHinted    = "hint"
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
	// Tool and CallID name a result's call. On a lead row, Tool is how
	// the lead was given (leadPrefilled, leadHinted).
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
	return kind == rowInput || kind == rowWords || kind == rowSummary || kind == rowDiary
}

// live is the rows the model sees: everything not compacted, oldest first.
func (t *transcript) live() ([]TranscriptRow, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return queryRows(t.db, `WHERE compacted = 0 ORDER BY turn, id`)
}

// all is every row, compacted or not, oldest first.
func (t *transcript) all() ([]TranscriptRow, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return queryRows(t.db, `ORDER BY turn, id`)
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
// the summary, then every finished turn. A folded day renders nothing:
// part 2 carries the last diaryDays of the diary, and the file the rest. A turn that failed or never finished
// is left out whole, input and all: the model never answered it, and a
// question with no answer after it reads as one still open. A reflection
// turn is left out too (reflectTurns).
func replay(rows []TranscriptRow) []model.Message {
	var out []model.Message
	ended := map[int64]string{}
	for _, r := range rows {
		if r.Ends() {
			ended[r.Turn] = r.Kind
		}
	}
	reflecting := reflectTurns(rows)
	for _, r := range rows {
		switch r.Kind {
		case rowSummary:
			out = append(out, model.Message{Role: "system", Content: "Earlier, in summary:\n" + r.Text})
			continue
		case rowDiary:
			continue
		}
		if end := ended[r.Turn]; end == "" || end == rowFailed || reflecting[r.Turn] {
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

// compaction is what compact needs besides the rows.
type compaction struct {
	// budget is the tokens the live rows may take (compactBudget).
	budget int
	// today is the day that is never folded (dayOf now).
	today string
	// entry is a day's diary entry, if it has one.
	entry func(day string) (string, bool)
	// reflect writes a day's entry now, when compaction needs to fold a
	// day that has none. Nil leaves such a day to the summary.
	reflect func(ctx context.Context, day string)
	// summarise makes the summary call; it is given the earlier summary
	// and the turns as text.
	summarise func(ctx context.Context, text string) (string, error)
}

// compact brings the live rows under the budget. Whole days first, oldest
// first: a day with a diary entry is folded behind it; a day the person
// and the ghost talked on that has none is reflected on now, the one time
// reflection does not wait for the person to be away, and folded once it
// has one; a day with no talk on it is folded with nothing in its place.
// A day that still has no entry is summarised on its own, into a summary
// row that belongs to it, so no summary spans two days and a later
// reflection reads it as the day's digest. Only when today is all that is
// left over the budget are today's older turns summarised, keeping the
// last compactKeep verbatim, with those past days' summaries rolled in. It reports the days folded and whether it
// summarised.
func (t *transcript) compact(ctx context.Context, c compaction) (folded []string, summarised bool, err error) {
	done := map[string]bool{}
	for {
		rows, err := t.live()
		if err != nil {
			return folded, summarised, err
		}
		if estimateTokens(replay(rows)) <= c.budget {
			return folded, summarised, nil
		}
		day := ""
		for _, d := range daysOf(rows) {
			if d < c.today && !done[d] {
				day = d
				break
			}
		}
		if day == "" {
			break
		}
		done[day] = true
		entry, ok := "", false
		if c.entry != nil {
			entry, ok = c.entry(day)
		}
		if !ok && talked(rows, day) {
			if c.reflect != nil {
				c.reflect(ctx, day)
				entry, ok = c.entry(day)
			}
			if !ok {
				did, err := t.summariseDay(ctx, day, c.summarise)
				if err != nil {
					return folded, summarised, err
				}
				summarised = summarised || did
				continue
			}
		}
		if err := t.compactDay(day, entry); err != nil {
			return folded, summarised, err
		}
		folded = append(folded, day)
	}
	did, err := t.summariseOlder(ctx, c.budget, c.today, c.entry, c.summarise)
	return folded, summarised || did, err
}

// summariseOlder summarises today's older turns when the live rows pass
// the budget, keeping the last compactKeep turns verbatim, and rolls in
// the summaries past days without an entry were left as, which nothing
// else can shrink. A day behind a diary entry is never touched: its only
// live row is the diary row, and hasEntry keeps any other out. The summary
// is stamped with the last turn it covers, so a call made after midnight
// stays on its day.
func (t *transcript) summariseOlder(ctx context.Context, budget int, today string, hasEntry func(day string) (string, bool), summarise func(ctx context.Context, text string) (string, error)) (bool, error) {
	rows, err := t.live()
	if err != nil {
		return false, err
	}
	if estimateTokens(replay(rows)) <= budget {
		return false, nil
	}
	days := rowDays(rows)
	var gone []TranscriptRow
	for _, r := range rows {
		day, ok := days[r.ID]
		if !ok || r.Kind != rowSummary || day >= today {
			continue
		}
		if hasEntry != nil {
			if _, has := hasEntry(day); has {
				continue
			}
		}
		gone = append(gone, r)
	}
	of := rowsOf(rows, today)
	var turns []int64
	seen := map[int64]bool{}
	for _, r := range of {
		if r.Kind != rowSummary && !seen[r.Turn] {
			seen[r.Turn] = true
			turns = append(turns, r.Turn)
		}
	}
	cut := int64(-1) // the first turn kept; none of today's go
	if len(turns) > compactKeep {
		cut = turns[len(turns)-compactKeep]
	}
	ownTurns := false
	for _, r := range of {
		if r.Kind == rowSummary || (cut >= 0 && r.Turn < cut) {
			gone = append(gone, r)
			ownTurns = ownTurns || r.Kind != rowSummary
		}
	}
	if !ownTurns && len(gone) < 2 {
		return false, nil // one summary alone is already as short as it gets
	}
	sort.SliceStable(gone, func(i, j int) bool {
		if gone[i].Turn != gone[j].Turn {
			return gone[i].Turn < gone[j].Turn
		}
		return gone[i].ID < gone[j].ID
	})
	var last int64
	var at time.Time
	for _, r := range gone {
		if r.Turn >= last {
			if r.Turn > last || at.IsZero() {
				at = r.At
			}
			last = r.Turn
		}
	}
	return t.summariseRows(ctx, gone, last, at, summarise)
}

// summariseDay summarises a day that could not be given a diary entry on
// its own: its rows are compacted and one summary row put in their place,
// on that day. A day that is already only a summary is left as it is.
func (t *transcript) summariseDay(ctx context.Context, day string, summarise func(ctx context.Context, text string) (string, error)) (bool, error) {
	rows, err := t.live()
	if err != nil {
		return false, err
	}
	of := rowsOf(rows, day)
	if !slices.ContainsFunc(of, func(r TranscriptRow) bool { return r.Kind != rowSummary }) {
		return false, nil
	}
	turn, at := dayAnchor(of)
	return t.summariseRows(ctx, of, turn, at, summarise)
}

// summariseRows has rows summarised, marks them compacted and puts the
// summary in their place as turn, at.
func (t *transcript) summariseRows(ctx context.Context, rows []TranscriptRow, turn int64, at time.Time, summarise func(ctx context.Context, text string) (string, error)) (bool, error) {
	var b strings.Builder
	for _, r := range rows {
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
	defer t.mu.Unlock()
	tx, err := t.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, err := tx.Exec(`UPDATE rows SET compacted = 1 WHERE id = ?`, r.ID); err != nil {
			return false, err
		}
	}
	if err := insertRow(tx, turn, rowSummary, summary, at); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// insertRow adds a summary or diary row and indexes it for recall.
func insertRow(tx *sql.Tx, turn int64, kind, text string, at time.Time) error {
	stamp := at.Format(time.RFC3339Nano)
	res, err := tx.Exec(`INSERT INTO rows (turn, kind, text, at) VALUES (?, ?, ?, ?)`, turn, kind, text, stamp)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	_, err = tx.Exec(`INSERT INTO transcript_fts (rowid, text, kind, turn, at) VALUES (?, ?, ?, ?, ?)`,
		id, text, kind, turn, stamp)
	return err
}

// dayAnchor is where a row standing for a day's rows goes: the day's last
// turn, at the start of that turn, which is on the day even when the turn
// ran past midnight. A day that is only a summary takes the summary's.
func dayAnchor(of []TranscriptRow) (int64, time.Time) {
	last, at := of[0].Turn, of[0].At
	for _, r := range of {
		if r.Kind != rowSummary && r.Turn > last {
			last, at = r.Turn, r.At
		}
	}
	return last, at
}

// compactTurn marks one turn's rows compacted: a reflection turn, which is
// never replayed and must not count toward the turns kept verbatim.
func (t *transcript) compactTurn(turn int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := t.db.Exec(`UPDATE rows SET compacted = 1 WHERE turn = ? AND kind != ? AND kind != ?`, turn, rowSummary, rowDiary)
	return err
}

// reflectTurns is the turns among rows that are the ghost reflecting on a
// day (kind=reflect): the turns that belong to no day and are not
// replayed.
func reflectTurns(rows []TranscriptRow) map[int64]bool {
	out := map[int64]bool{}
	for _, r := range rows {
		if r.Kind == rowEvent && EnvelopeKind(r.Text) == reflectKind {
			out[r.Turn] = true
		}
	}
	return out
}

// rowDays is the day of each row that has one, by row ID: a turn's rows
// all fall on the day it began, a summary and a diary row on their own.
// Reflection turns have none.
func rowDays(rows []TranscriptRow) map[int64]string {
	began := map[int64]string{}
	reflecting := reflectTurns(rows)
	out := map[int64]string{}
	for _, r := range rows {
		switch {
		case r.Kind == rowSummary || r.Kind == rowDiary:
			out[r.ID] = dayOf(r.At)
		case reflecting[r.Turn]:
		default:
			if _, ok := began[r.Turn]; !ok {
				began[r.Turn] = dayOf(r.At)
			}
			out[r.ID] = began[r.Turn]
		}
	}
	return out
}

// daysOf is the days rows hold besides their diary rows, oldest first.
func daysOf(rows []TranscriptRow) []string {
	days := rowDays(rows)
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if day, ok := days[r.ID]; ok && r.Kind != rowDiary && !seen[day] {
			seen[day] = true
			out = append(out, day)
		}
	}
	sort.Strings(out)
	return out
}

// days is the days the live transcript holds, oldest first.
func (t *transcript) days() ([]string, error) {
	rows, err := t.live()
	if err != nil {
		return nil, err
	}
	return daysOf(rows), nil
}

// rowsOf is a day's rows, diary rows left out, in order.
func rowsOf(rows []TranscriptRow, day string) []TranscriptRow {
	days := rowDays(rows)
	var out []TranscriptRow
	for _, r := range rows {
		if d, ok := days[r.ID]; ok && d == day && r.Kind != rowDiary {
			out = append(out, r)
		}
	}
	return out
}

// talked reports whether the person or the ghost said anything on day,
// or a summary carries what they did: a day of events answered with
// silence has nothing to write an entry about.
func talked(rows []TranscriptRow, day string) bool {
	for _, r := range rowsOf(rows, day) {
		if r.Kind == rowInput || r.Kind == rowWords || r.Kind == rowSummary {
			return true
		}
	}
	return false
}

// compactDay folds a day: its live rows are marked compacted and, when
// entry is not empty, one diary row carries it in their place, dated on
// that day and numbered with its last turn so it sorts where the day was.
func (t *transcript) compactDay(day, entry string) error {
	rows, err := t.live()
	if err != nil {
		return err
	}
	of := rowsOf(rows, day)
	if len(of) == 0 {
		return nil
	}
	turn, at := dayAnchor(of)
	t.mu.Lock()
	defer t.mu.Unlock()
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range of {
		if _, err := tx.Exec(`UPDATE rows SET compacted = 1 WHERE id = ?`, r.ID); err != nil {
			return err
		}
	}
	if entry = strings.TrimSpace(entry); entry != "" {
		if err := insertRow(tx, turn, rowDiary, entry, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// digestLine is one row as the summary call reads it.
func digestLine(r TranscriptRow) string {
	switch r.Kind {
	case rowSummary:
		return "(summary so far) " + r.Text
	case rowDiary:
		return "(diary " + dayOf(r.At) + ") " + r.Text
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
	// A lead row is not digested: a summary or a diary would tell the
	// model it was led.
	return ""
}
