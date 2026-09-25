package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Daily message export.
//
// The goal is a folder per day that an AI agent can read to summarise activity:
// a structured JSON file, a human/LLM-readable Markdown transcript, and copies
// of every image and attachment.
//
// Capture is a continuous journal. Every receive runs with --output=json, and
// each message is appended to a per-day JSONL journal as it arrives, so nothing
// is missed between exports or lost to a once-a-day pull. A journal line is
// written for both directions:
//
//   - dataMessage           — a message received by this account
//   - syncMessage.sentMessage — a message this account SENT from another linked
//     device (phone), which a linked signal-cli receives as a sync
//
// The daily bundle is then assembled from the journal: parse the day, copy the
// referenced attachments out of signal-cli's store, and render JSON + Markdown.

// --- raw signal-cli JSON shapes ---------------------------------------------
//
// Only the fields used here are declared; everything else is ignored. Nested
// messages are pointers so their absence is distinguishable from an empty value.

type rxLine struct {
	Envelope rxEnvelope `json:"envelope"`
	Account  string     `json:"account"`
}

type rxEnvelope struct {
	Source       string  `json:"source"`
	SourceNumber string  `json:"sourceNumber"`
	SourceUUID   string  `json:"sourceUuid"`
	SourceName   string  `json:"sourceName"`
	Timestamp    int64   `json:"timestamp"`
	DataMessage  *rxData `json:"dataMessage"`
	SyncMessage  *rxSync `json:"syncMessage"`
}

type rxSync struct {
	SentMessage *rxSent `json:"sentMessage"`
}

type rxSent struct {
	Destination       string     `json:"destination"`
	DestinationNumber string     `json:"destinationNumber"`
	DestinationUUID   string     `json:"destinationUuid"`
	Timestamp         int64      `json:"timestamp"`
	Message           string     `json:"message"`
	GroupInfo         *rxGroup   `json:"groupInfo"`
	Attachments       []rxAttach `json:"attachments"`
}

type rxData struct {
	Message     string     `json:"message"`
	Timestamp   int64      `json:"timestamp"`
	GroupInfo   *rxGroup   `json:"groupInfo"`
	Attachments []rxAttach `json:"attachments"`
}

type rxGroup struct {
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
}

type rxAttach struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// --- normalized journal record ----------------------------------------------

// JournalMessage is one message in a form that is stable, self-describing, and
// easy for an agent to consume. It is what gets written to the JSONL journal and
// to the day's messages.json.
type JournalMessage struct {
	Account        string              `json:"account"`         // which of the user's numbers
	Direction      string              `json:"direction"`       // "received" | "sent"
	TimestampMS    int64               `json:"timestamp_ms"`    // Signal message timestamp
	Time           string              `json:"time"`            // RFC3339, local
	ConversationID string              `json:"conversation_id"` // stable thread key (both directions)
	Conversation   string              `json:"conversation"`    // human thread label (contact or group)
	IsGroup        bool                `json:"is_group"`        // true when a group thread
	From           string              `json:"from"`            // sender display name (Signal profile name, else number)
	FromNumber     string              `json:"from_number,omitempty"`
	FromUUID       string              `json:"from_uuid,omitempty"`
	To             string              `json:"to,omitempty"` // other party for 1:1
	Body           string              `json:"body"`
	Attachments    []JournalAttachment `json:"attachments,omitempty"`
}

type JournalAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	// File is the bundle-relative path to the copied file, filled in when the
	// daily bundle is built. Empty (with a note) if the file was not available.
	File string `json:"file,omitempty"`
	Note string `json:"note,omitempty"`
}

// --- paths ------------------------------------------------------------------

// exportJournalDir holds the append-only per-day JSONL journals. It lives in the
// app data folder regardless of where bundles go, so capture never depends on a
// user-configured path being present.
func exportJournalDir() string {
	return filepath.Join(appDataDir(), "exports", "journal")
}

func journalPathForDate(day string) string {
	return filepath.Join(exportJournalDir(), day+".jsonl")
}

// exportBundleRoot is where finished daily bundles are written. It is the
// user-chosen folder when set, otherwise a default under the app data folder.
func exportBundleRoot(cfg Config) string {
	if strings.TrimSpace(cfg.ExportDir) != "" {
		return expandHome(cfg.ExportDir)
	}
	return filepath.Join(appDataDir(), "exports", "bundles")
}

// signalAttachmentsDir is where signal-cli stores downloaded attachment files,
// each named by its attachment id.
func signalAttachmentsDir() string {
	return filepath.Join(signalCLIDataDir(), "attachments")
}

// groupNamesCachePath maps groupId -> group name. Received messages frequently
// omit the group name (signal-cli's receive JSON carries only the group id), so
// the real names are resolved separately via `listGroups` and cached here, then
// used to label group folders.
func groupNamesCachePath() string {
	return filepath.Join(appDataDir(), "exports", "group-names.json")
}

// rxGroupListItem is one entry of `signal-cli listGroups --output=json`.
type rxGroupListItem struct {
	ID   string `json:"id"`   // base64 groupId, matching groupInfo.groupId
	Name string `json:"name"` // the group's display name
}

// updateGroupNamesFromListJSON merges the names from one listGroups JSON array
// into the on-disk cache. Safe to call often; it only adds or updates names.
func updateGroupNamesFromListJSON(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '[' {
		return
	}
	var items []rxGroupListItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return
	}
	names := loadGroupNames()
	changed := false
	for _, it := range items {
		if it.ID != "" && strings.TrimSpace(it.Name) != "" && names[it.ID] != it.Name {
			names[it.ID] = it.Name
			changed = true
		}
	}
	if changed {
		saveGroupNames(names)
	}
}

func loadGroupNames() map[string]string {
	names := map[string]string{}
	data, err := os.ReadFile(groupNamesCachePath())
	if err != nil {
		return names
	}
	_ = json.Unmarshal(data, &names)
	return names
}

func saveGroupNames(names map[string]string) {
	if err := os.MkdirAll(filepath.Dir(groupNamesCachePath()), 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return
	}
	tmp := groupNamesCachePath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, groupNamesCachePath())
	}
}

// --- capture ----------------------------------------------------------------

// rpcLine is one line of signal-cli's jsonRpc stdio stream: either a "receive"
// notification carrying a message, or a response to a request we sent (e.g.
// listGroups), whose result is the array we cache group names from.
type rpcLine struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

// processDaemonLine handles one line from a persistent jsonRpc daemon. A receive
// notification's params are the same {envelope, account} shape as batch receive
// output, so the same normalizer is reused. A response's result, when it is a
// group array, refreshes the group-name cache. account is the daemon's account,
// used as a fallback when the line omits it.
func processDaemonLine(account string, line []byte, exporting bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var rl rpcLine
	if json.Unmarshal(line, &rl) != nil {
		return
	}
	switch {
	case rl.Method == "receive" && len(rl.Params) > 0:
		if exporting {
			journalReceiveParams(account, rl.Params)
		}
	case len(rl.Result) > 0:
		updateGroupNamesFromListJSON(string(rl.Result))
	}
}

// journalReceiveParams normalizes and journals one message from a jsonRpc
// receive notification's params ({envelope, account}). Returns whether a message
// was journaled.
func journalReceiveParams(account string, params []byte) bool {
	var rl rxLine
	if json.Unmarshal(params, &rl) != nil {
		return false
	}
	msg, ok := normalizeMessage(account, rl)
	if !ok {
		return false
	}
	return appendJournal(msg) == nil
}

// journalReceiveOutput parses one receive's JSON output and appends a journal
// line for every message (sent or received). Unparseable or non-message lines
// are skipped. It returns how many messages were journaled.
func journalReceiveOutput(account, raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	n := 0
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024) // messages can carry long bodies
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue // signal-cli also prints plain INFO lines; skip them
		}
		var rl rxLine
		if err := json.Unmarshal([]byte(line), &rl); err != nil {
			continue
		}
		msg, ok := normalizeMessage(account, rl)
		if !ok {
			continue
		}
		if err := appendJournal(msg); err == nil {
			n++
		}
	}
	return n
}

// normalizeMessage turns a raw receive line into a JournalMessage, or reports
// ok=false when the line carries no user-visible message (receipts, typing,
// other sync types).
func normalizeMessage(account string, rl rxLine) (JournalMessage, bool) {
	env := rl.Envelope
	acct := rl.Account
	if acct == "" {
		acct = account
	}

	// Sent-from-another-device (sync) first: this is how a linked signal-cli
	// sees messages the user sent from their phone.
	if env.SyncMessage != nil && env.SyncMessage.SentMessage != nil {
		s := env.SyncMessage.SentMessage
		if s.Message == "" && len(s.Attachments) == 0 {
			return JournalMessage{}, false
		}
		ts := s.Timestamp
		if ts == 0 {
			ts = env.Timestamp
		}
		m := JournalMessage{
			Account:     acct,
			Direction:   "sent",
			TimestampMS: ts,
			Time:        localTime(ts),
			From:        "Me",
			Body:        s.Message,
			Attachments: convertAttachments(s.Attachments),
		}
		applyThread(&m, s.GroupInfo, s.DestinationNumber, displayName("", s.DestinationNumber))
		m.To = m.Conversation
		return m, true
	}

	// Received message.
	if env.DataMessage != nil {
		d := env.DataMessage
		if d.Message == "" && len(d.Attachments) == 0 {
			return JournalMessage{}, false
		}
		ts := d.Timestamp
		if ts == 0 {
			ts = env.Timestamp
		}
		// Identify the sender as robustly as signal-cli allows. There is no
		// Signal @username in receive output, so the display name is the profile
		// name the sender set, falling back to their number, then a short label
		// derived from their UUID — so a sender is never blank, which matters in
		// groups where several people speak.
		number := firstNonEmpty(env.SourceNumber, env.Source)
		sender := senderDisplay(env.SourceName, number, env.SourceUUID)
		m := JournalMessage{
			Account:     acct,
			Direction:   "received",
			TimestampMS: ts,
			Time:        localTime(ts),
			From:        sender,
			FromNumber:  number,
			FromUUID:    env.SourceUUID,
			Body:        d.Message,
			Attachments: convertAttachments(d.Attachments),
		}
		applyThread(&m, d.GroupInfo, number, sender)
		return m, true
	}

	return JournalMessage{}, false
}

// applyThread sets the conversation label, group flag, and internal grouping key
// so both directions of a 1:1 chat share one conversation.
func applyThread(m *JournalMessage, g *rxGroup, otherNumber, otherDisplay string) {
	if g != nil && g.GroupID != "" {
		m.IsGroup = true
		name := g.GroupName
		if name == "" {
			name = "Group " + shortID(g.GroupID)
		}
		m.Conversation = name
		m.ConversationID = "group:" + g.GroupID
		return
	}
	m.IsGroup = false
	m.ConversationID = "dm:" + strings.ToLower(otherNumber)
	if otherDisplay != "" {
		m.Conversation = otherDisplay
	} else if otherNumber != "" {
		m.Conversation = otherNumber
	} else {
		m.Conversation = "Unknown"
	}
}

func convertAttachments(in []rxAttach) []JournalAttachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]JournalAttachment, 0, len(in))
	for _, a := range in {
		out = append(out, JournalAttachment{
			ID:          a.ID,
			Filename:    a.Filename,
			ContentType: a.ContentType,
		})
	}
	return out
}

// appendJournal appends one message to the journal for the day its timestamp
// falls on (local time). Append-only, so concurrent receives never clobber.
func appendJournal(m JournalMessage) error {
	if err := os.MkdirAll(exportJournalDir(), 0o700); err != nil {
		return err
	}
	day := time.UnixMilli(m.TimestampMS).Local().Format("2006-01-02")
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(journalPathForDate(day), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// --- bundling ---------------------------------------------------------------

// conversationBundle collects one chat's messages while a day is assembled.
type conversationBundle struct {
	id       string
	label    string
	isGroup  bool
	messages []JournalMessage
}

// BuildDailyBundle assembles a day (YYYY-MM-DD) from its journal into a folder
// per account, and within each a folder per day, and within each a folder per
// chat holding that chat's messages.json, transcript.md, and attachments:
//
//	<export root>/<account>/<date>/<chat>/messages.json
//	                                       transcript.md
//	                                       attachments/
//	<export root>/<account>/<date>/index.md   (that day's chats at a glance)
//
// It is safe to call repeatedly; it rewrites from the journal each time, which
// is why today can be refreshed as more messages arrive. Returns the export
// root, the total messages written, or an error.
func BuildDailyBundle(cfg Config, day string) (string, int, error) {
	msgs, err := readJournal(day)
	if err != nil {
		return "", 0, err
	}
	root := exportBundleRoot(cfg)

	// Group account -> conversation.
	byAccount := map[string]map[string]*conversationBundle{}
	for _, m := range msgs {
		convs, ok := byAccount[m.Account]
		if !ok {
			convs = map[string]*conversationBundle{}
			byAccount[m.Account] = convs
		}
		key := m.ConversationID
		if key == "" {
			key = m.Conversation
		}
		c, ok := convs[key]
		if !ok {
			c = &conversationBundle{id: key, label: m.Conversation, isGroup: m.IsGroup}
			convs[key] = c
		}
		// A 1:1 thread is seen from both sides; only the received side carries
		// the contact name, so keep the better label.
		if betterLabel(c.label, m.Conversation) {
			c.label = m.Conversation
		}
		c.messages = append(c.messages, m)
	}

	// Resolve real group names from the cache (receive JSON often omits them).
	groupNames := loadGroupNames()
	for _, convs := range byAccount {
		for _, c := range convs {
			if c.isGroup {
				gid := strings.TrimPrefix(c.id, "group:")
				if name := groupNames[gid]; strings.TrimSpace(name) != "" {
					c.label = name
				}
			}
		}
	}

	total := 0
	// Deterministic account order.
	accountKeys := sortedKeys(byAccount)
	for _, acct := range accountKeys {
		acctDayDir := filepath.Join(root, accountFolderName(cfg, acct), day)
		convs := byAccount[acct]

		// Deterministic conversation order: by label, then id.
		convKeys := make([]string, 0, len(convs))
		for k := range convs {
			convKeys = append(convKeys, k)
		}
		sort.Slice(convKeys, func(i, j int) bool {
			li, lj := convs[convKeys[i]].label, convs[convKeys[j]].label
			if li == lj {
				return convKeys[i] < convKeys[j]
			}
			return li < lj
		})

		usedFolders := map[string]bool{}
		var index []convIndexEntry
		for _, k := range convKeys {
			c := convs[k]
			sort.SliceStable(c.messages, func(i, j int) bool {
				return c.messages[i].TimestampMS < c.messages[j].TimestampMS
			})

			folder := convFolderName(c.label, c.id, usedFolders)
			chatDir := filepath.Join(acctDayDir, folder)
			attachDir := filepath.Join(chatDir, "attachments")
			if err := os.MkdirAll(attachDir, 0o755); err != nil {
				return "", 0, err
			}

			usedAtt := map[string]bool{}
			for i := range c.messages {
				for j := range c.messages[i].Attachments {
					a := &c.messages[i].Attachments[j]
					rel, note := copyAttachment(*a, attachDir, usedAtt)
					a.File = rel
					a.Note = note
				}
			}

			if err := writeChatJSON(chatDir, day, acct, c); err != nil {
				return "", 0, err
			}
			if err := writeChatTranscript(chatDir, c); err != nil {
				return "", 0, err
			}

			kind := "Direct"
			if c.isGroup {
				kind = "Group"
			}
			index = append(index, convIndexEntry{
				Label: c.label, Folder: folder, Kind: kind, Count: len(c.messages),
			})
			total += len(c.messages)
		}

		if err := writeAccountIndex(acctDayDir, day, acct, index); err != nil {
			return "", 0, err
		}
	}

	return root, total, nil
}

func readJournal(day string) ([]JournalMessage, error) {
	f, err := os.Open(journalPathForDate(day))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no messages that day is not an error
		}
		return nil, err
	}
	defer f.Close()

	var msgs []JournalMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m JournalMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, sc.Err()
}

// copyAttachment copies one attachment out of signal-cli's store into the
// bundle. Returns the bundle-relative path, or a note explaining why it could
// not be copied. Missing files are common and not fatal — signal-cli may not
// have downloaded an attachment, or it may have been pruned.
func copyAttachment(a JournalAttachment, attachDir string, used map[string]bool) (string, string) {
	if a.ID == "" {
		return "", "no attachment id"
	}
	src := filepath.Join(signalAttachmentsDir(), a.ID)
	if !fileExists(src) {
		return "", "not downloaded by signal-cli"
	}

	name := attachmentName(a, used)
	dst := filepath.Join(attachDir, name)
	if err := copyFile(src, dst); err != nil {
		return "", "copy failed: " + err.Error()
	}
	return "attachments/" + name, ""
}

// attachmentName produces a readable, unique, filesystem-safe name for a copied
// attachment.
func attachmentName(a JournalAttachment, used map[string]bool) string {
	base := a.Filename
	if base == "" {
		base = a.ID
		if ext := extensionForType(a.ContentType); ext != "" {
			base += ext
		}
	}
	base = sanitizeFilename(base)

	name := base
	for i := 1; used[strings.ToLower(name)]; i++ {
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		name = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
	used[strings.ToLower(name)] = true
	return name
}

// writeChatJSON writes one conversation's structured messages.json.
func writeChatJSON(chatDir, day, account string, c *conversationBundle) error {
	type doc struct {
		Account        string           `json:"account"`
		Date           string           `json:"date"`
		Conversation   string           `json:"conversation"`
		ConversationID string           `json:"conversation_id"`
		IsGroup        bool             `json:"is_group"`
		Generated      string           `json:"generated"`
		MessageCount   int              `json:"message_count"`
		Messages       []JournalMessage `json:"messages"`
	}
	d := doc{
		Account:        account,
		Date:           day,
		Conversation:   c.label,
		ConversationID: c.id,
		IsGroup:        c.isGroup,
		Generated:      time.Now().Format(time.RFC3339),
		MessageCount:   len(c.messages),
		Messages:       c.messages,
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(chatDir, "messages.json"), data, 0o644)
}

// writeChatTranscript writes one conversation's readable transcript.
func writeChatTranscript(chatDir string, c *conversationBundle) error {
	kind := "Direct"
	if c.isGroup {
		kind = "Group"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (%s)\n\n%d messages. Times are local.\n\n",
		c.label, kind, len(c.messages))

	for _, m := range c.messages {
		t := time.UnixMilli(m.TimestampMS).Local().Format("2006-01-02 15:04")
		who := m.From
		if m.Direction == "sent" {
			who = "Me"
		}
		fmt.Fprintf(&b, "- **%s** %s", who, t)
		if strings.TrimSpace(m.Body) != "" {
			fmt.Fprintf(&b, ": %s", oneLine(m.Body))
		}
		b.WriteString("\n")
		for _, a := range m.Attachments {
			switch {
			case a.File != "" && isImageType(a.ContentType):
				fmt.Fprintf(&b, "    - image: ![%s](%s)\n", a.displayLabel(), a.File)
			case a.File != "":
				fmt.Fprintf(&b, "    - attachment: [%s](%s)\n", a.displayLabel(), a.File)
			default:
				fmt.Fprintf(&b, "    - attachment: %s (%s)\n", a.displayLabel(), a.Note)
			}
		}
	}
	return os.WriteFile(filepath.Join(chatDir, "transcript.md"), []byte(b.String()), 0o644)
}

// convIndexEntry is one row of an account-day index.
type convIndexEntry struct {
	Label  string
	Folder string
	Kind   string
	Count  int
}

// writeAccountIndex writes index.md summarising the day's chats for one account,
// so an agent (or a person) can see the day at a glance and open a chat folder.
func writeAccountIndex(acctDayDir, day, account string, entries []convIndexEntry) error {
	if err := os.MkdirAll(acctDayDir, 0o755); err != nil {
		return err
	}
	total := 0
	for _, e := range entries {
		total += e.Count
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", account, day)
	fmt.Fprintf(&b, "%d messages across %d conversations. Each chat is a folder "+
		"with its own transcript.md, messages.json, and attachments.\n\n",
		total, len(entries))
	if len(entries) == 0 {
		b.WriteString("_No messages this day._\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "- **%s** (%s) — %d messages → [`%s/`](%s/transcript.md)\n",
			e.Label, e.Kind, e.Count, e.Folder, e.Folder)
	}
	return os.WriteFile(filepath.Join(acctDayDir, "index.md"), []byte(b.String()), 0o644)
}

// accountFolderName is the per-account folder: the account's label when set,
// otherwise its phone number. Sanitized for the filesystem.
func accountFolderName(cfg Config, number string) string {
	label := ""
	for _, a := range cfg.Accounts {
		if a != nil && a.Number == number {
			label = a.Label
			break
		}
	}
	name := sanitizeFilename(label)
	if name == "" || name == "file" {
		name = sanitizeFilename(number)
	}
	if name == "" || name == "file" {
		name = "account"
	}
	return name
}

// convFolderName is the per-chat folder: the conversation label, made unique
// within the account-day by appending a short stable hash of the conversation
// id on collision (two different chats that happen to share a display name).
func convFolderName(label, id string, used map[string]bool) string {
	base := sanitizeFilename(label)
	if base == "" || base == "file" {
		base = sanitizeFilename(id)
	}
	if base == "" || base == "file" {
		base = "conversation"
	}
	name := base
	if used[strings.ToLower(name)] {
		name = base + "-" + shortHash(id)
	}
	for i := 2; used[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s-%s-%d", base, shortHash(id), i)
	}
	used[strings.ToLower(name)] = true
	return name
}

func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())[:6]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- orchestration ----------------------------------------------------------

// buildPendingBundles builds bundles for every journaled day that does not yet
// have one, plus always rebuilds today (which is still accumulating). This runs
// at startup so days the app was closed over still get exported, and on the
// daily timer.
func buildPendingBundles(cfg Config) {
	entries, err := os.ReadDir(exportJournalDir())
	if err != nil {
		return
	}
	today := time.Now().Local().Format("2006-01-02")
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(name, ".jsonl")
		if day != today && fileExists(builtMarkerPath(day)) {
			continue // a past, completed day already built; leave it
		}
		if _, _, err := BuildDailyBundle(cfg, day); err == nil && day != today {
			writeBuiltMarker(day)
		}
	}
}

// builtMarkerPath records that a past day was fully built. It lives in the app
// data folder, not the user's export folder, so the export folder stays clean
// and the marker survives changing the export location.
func builtMarkerPath(day string) string {
	return filepath.Join(appDataDir(), "exports", "built", day)
}

func writeBuiltMarker(day string) {
	dir := filepath.Dir(builtMarkerPath(day))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(builtMarkerPath(day), []byte(time.Now().Format(time.RFC3339)), 0o600)
}

// --- small helpers ----------------------------------------------------------

// betterLabel reports whether candidate is a better conversation label than
// current: a name (not starting with +) beats a bare number, and any non-empty
// label beats an empty one.
func betterLabel(current, candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return true
	}
	currentIsNumber := strings.HasPrefix(current, "+")
	candIsNumber := strings.HasPrefix(candidate, "+")
	return currentIsNumber && !candIsNumber
}

func (a JournalAttachment) displayLabel() string {
	if a.Filename != "" {
		return a.Filename
	}
	if a.ContentType != "" {
		return a.ContentType
	}
	return a.ID
}

func localTime(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).Local().Format(time.RFC3339)
}

func displayName(name, number string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return number
}

// senderDisplay resolves a never-empty label for who sent a message: the Signal
// profile name if shared, else the phone number, else a short label derived from
// the sender's UUID. signal-cli receive output carries no @username, so the
// profile name is the closest thing to one.
func senderDisplay(name, number, uuid string) string {
	if s := strings.TrimSpace(name); s != "" {
		return s
	}
	if s := strings.TrimSpace(number); s != "" {
		return s
	}
	if s := strings.TrimSpace(uuid); s != "" {
		return "user " + shortID(s)
	}
	return "Unknown"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func shortID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func isImageType(ct string) bool {
	return strings.HasPrefix(strings.ToLower(ct), "image/")
}

func extensionForType(ct string) string {
	switch strings.ToLower(strings.TrimSpace(ct)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/heic":
		return ".heic"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "audio/aac", "audio/mp4":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == ' ', r == '+', r == '(', r == ')':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "file"
	}
	if len(out) > 120 {
		out = out[len(out)-120:]
	}
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
