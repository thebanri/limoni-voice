// Room view: chat message parsing, wrapping, text selection and mouse handling.

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/thebanri/limoni-voice/internal/i18n"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
)

type chatSpan struct {
	Text     string
	IsLink   bool
	ClickURL string
	IsCopy   bool
	CopyText string
}

type roomDisplayLine struct {
	Timestamp      string
	Badge          string
	BadgeStyle     cell.Style
	Text           string
	TextStyle      cell.Style
	Spans          []chatSpan
	IsChat         bool
	IsContinuation bool
	Glue           string // whitespace dropped where a continuation was wrapped, put back when copied
	RawMessage     string
}

// chatTab is how a tab is shown: a tab cell has no width of its own, so it vanished.
const chatTab = "    "

var (
	reChatURL   = regexp.MustCompile(`https?://[^\s<>"]+|www\.[^\s<>"]+`)
	reChatCopy1 = regexp.MustCompile(`(?s)📋\s*\[(?:Kopyala|Copy|COPY):\s*([^\]]+)\]`)
	reChatCopy2 = regexp.MustCompile(`(?s)\[(?:kopyala|copy|KOPYALA|COPY|Kopyala|Copy):\s*([^\]]+)\]`)
	reChatCopy3 = regexp.MustCompile(`copy://([^\s<>"]+)`)
	// reChatCopyWhole is a whole message made by /copy: everything up to its last ] is the
	// text, ] included. The patterns above stop at the first ], which cut a pasted log
	// ("[15:43:24] …") to its first few characters.
	reChatCopyWhole = regexp.MustCompile(`(?s)^\s*📋\s*\[(?:Kopyala|Copy|COPY):\s*(.*)\]\s*$`)
)

// messageCopyText returns the text a click on a copy message copies, or "".
func messageCopyText(raw string) string {
	for _, re := range []*regexp.Regexp{reChatCopyWhole, reChatCopy1, reChatCopy2, reChatCopy3} {
		if m := re.FindStringSubmatch(raw); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func cleanClickURL(raw string) string {
	clean := strings.TrimSpace(raw)
	for len(clean) > 0 {
		last := clean[len(clean)-1]
		if last == '.' || last == ',' || last == '!' || last == '?' || last == ';' || last == ':' || last == ')' || last == ']' || last == '>' || last == '"' || last == '\'' {
			clean = clean[:len(clean)-1]
		} else {
			break
		}
	}
	return clean
}

func splitWordsAndSpaces(s string) []string {
	var tokens []string
	var current strings.Builder
	var inSpace bool

	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace && current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			inSpace = true
			current.WriteRune(r)
		} else {
			if inSpace && current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			inSpace = false
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

type spanMatch struct {
	start    int
	end      int
	isLink   bool
	clickURL string
	isCopy   bool
	copyText string
}

func parseMessageSpans(text string) []chatSpan {
	// A /copy message is one copy button from end to end, whatever it holds.
	if m := reChatCopyWhole.FindStringSubmatch(text); len(m) > 1 {
		return []chatSpan{{Text: text, IsCopy: true, CopyText: m[1]}}
	}
	var matches []spanMatch

	// 1. Copy patterns (A: 📋 [Kopyala: ...], B: [copy: ...], C: copy://...)
	copyLocs1 := reChatCopy1.FindAllStringSubmatchIndex(text, -1)
	for _, loc := range copyLocs1 {
		copyVal := text[loc[2]:loc[3]]
		matches = append(matches, spanMatch{
			start:    loc[0],
			end:      loc[1],
			isCopy:   true,
			copyText: copyVal,
		})
	}

	copyLocs2 := reChatCopy2.FindAllStringSubmatchIndex(text, -1)
	for _, loc := range copyLocs2 {
		copyVal := text[loc[2]:loc[3]]
		matches = append(matches, spanMatch{
			start:    loc[0],
			end:      loc[1],
			isCopy:   true,
			copyText: copyVal,
		})
	}

	copyLocs3 := reChatCopy3.FindAllStringSubmatchIndex(text, -1)
	for _, loc := range copyLocs3 {
		copyVal := text[loc[2]:loc[3]]
		matches = append(matches, spanMatch{
			start:    loc[0],
			end:      loc[1],
			isCopy:   true,
			copyText: copyVal,
		})
	}

	// 2. URLs
	urlLocs := reChatURL.FindAllStringIndex(text, -1)
	for _, loc := range urlLocs {
		raw := text[loc[0]:loc[1]]
		clean := cleanClickURL(raw)
		matches = append(matches, spanMatch{
			start:    loc[0],
			end:      loc[1],
			isLink:   true,
			clickURL: clean,
		})
	}

	if len(matches) == 0 {
		return []chatSpan{{Text: text, IsLink: false, IsCopy: false}}
	}

	// Sort matches by start index ascending; longer match first if starts match
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].start == matches[j].start {
			return (matches[i].end - matches[i].start) > (matches[j].end - matches[j].start)
		}
		return matches[i].start < matches[j].start
	})

	// Filter out overlapping matches
	var filtered []spanMatch
	lastEnd := 0
	for _, m := range matches {
		if m.start >= lastEnd {
			filtered = append(filtered, m)
			lastEnd = m.end
		}
	}

	var spans []chatSpan
	lastIdx := 0
	for _, m := range filtered {
		if m.start > lastIdx {
			spans = append(spans, chatSpan{
				Text:   text[lastIdx:m.start],
				IsLink: false,
				IsCopy: false,
			})
		}
		spans = append(spans, chatSpan{
			Text:     text[m.start:m.end],
			IsLink:   m.isLink,
			ClickURL: m.clickURL,
			IsCopy:   m.isCopy,
			CopyText: m.copyText,
		})
		lastIdx = m.end
	}
	if lastIdx < len(text) {
		spans = append(spans, chatSpan{
			Text:   text[lastIdx:],
			IsLink: false,
			IsCopy: false,
		})
	}
	return spans
}

// wrapSpansToLines breaks one paragraph into lines of at most availWidth cells (an emoji
// takes two). glue[i] is the whitespace the wrap dropped before line i, which a copy puts
// back between it and the line before; after a word split for being too long, or a line
// that kept its space, it is empty. Leading spaces of the paragraph
// (indented code) are kept; those of a wrapped line are not.
func wrapSpansToLines(spans []chatSpan, availWidth int) (lines [][]chatSpan, glue []string) {
	availWidth = max(availWidth, 5)
	var cur []chatSpan
	curW := 0
	curGlue := ""

	flush := func() {
		if len(cur) > 0 {
			lines = append(lines, cur)
			glue = append(glue, curGlue)
		}
		cur, curW, curGlue = nil, 0, ""
	}
	add := func(span chatSpan, w int) {
		if n := len(cur); n > 0 && !cur[n-1].IsLink && !cur[n-1].IsCopy && !span.IsLink && !span.IsCopy {
			cur[n-1].Text += span.Text
		} else {
			cur = append(cur, span)
		}
		curW += w
	}
	// place puts a token that has no break inside it, splitting it by width only when it is
	// wider than a whole line.
	place := func(span chatSpan) {
		rs := []rune(span.Text)
		w := cell.StringWidth(span.Text)
		if curW+w > availWidth && curW > 0 {
			flush()
		}
		for curW+w > availWidth {
			n := fitRunes(rs, availWidth-curW)
			part := span
			part.Text = string(rs[:n])
			pw := cell.StringWidth(part.Text)
			add(part, pw)
			flush()
			rs = rs[n:]
			w -= pw // not measured again: that made a long unbroken text quadratic
		}
		if len(rs) > 0 {
			part := span
			part.Text = string(rs)
			add(part, w)
		}
	}

	for _, span := range spans {
		if span.IsLink || span.IsCopy {
			place(span)
			continue
		}
		for _, tok := range splitWordsAndSpaces(span.Text) {
			if !unicode.IsSpace([]rune(tok)[0]) {
				place(chatSpan{Text: tok})
				continue
			}
			w := cell.StringWidth(tok)
			switch {
			case curW == 0 && len(lines) > 0:
				curGlue += tok // a wrapped line does not start with the space it broke at
			case curW+w <= availWidth:
				add(chatSpan{Text: tok}, w)
			case curW == 0:
				place(chatSpan{Text: tok}) // indentation wider than the panel
			default:
				flush()
				curGlue = tok
			}
		}
	}
	flush()
	if len(lines) == 0 {
		lines, glue = [][]chatSpan{{{Text: ""}}}, []string{""}
	}
	return lines, glue
}

// fitRunesWithin returns how many of rs fit in width cells (possibly none).
func fitRunesWithin(rs []rune, width int) int {
	w := 0
	for i, r := range rs {
		w += cell.RuneWidth(r)
		if w > width {
			return i
		}
	}
	return len(rs)
}

// fitRunes returns how many of rs fit in width cells, at least one so a wrap always advances.
func fitRunes(rs []rune, width int) int {
	w := 0
	for i, r := range rs {
		w += cell.RuneWidth(r)
		if w > width {
			return max(i, 1)
		}
	}
	return len(rs)
}

// wrapWordsToLines is wrapSpansToLines for plain text.
func wrapWordsToLines(text string, maxW int) (lines []string, glue []string) {
	spanLines, glue := wrapSpansToLines([]chatSpan{{Text: text}}, maxW)
	for _, l := range spanLines {
		var sb strings.Builder
		for _, sp := range l {
			sb.WriteString(sp.Text)
		}
		lines = append(lines, sb.String())
	}
	return lines, glue
}

// lineCacheKey names a message's wrapped lines: they change with its width, the theme (their
// styles) and the language ("You:", translated logs).
type lineCacheKey struct {
	ts       int64
	sender   string
	senderID string
	text     string
	isChat   bool
	isSelf   bool
	width    int
	theme    string
	lang     i18n.Lang
}

// buildDisplayLines wraps the messages into screen lines. Each message's lines are kept from
// the frame before, so a very long message (a pasted log) is wrapped once, not 30 times a
// second.
func (r *RoomView) buildDisplayLines(messages []RoomMessage, maxW int) []roomDisplayLine {
	if maxW < 10 {
		maxW = 10
	}
	theme := CurrentTheme()
	lang := i18n.Current()

	r.lineCacheMu.Lock()
	defer r.lineCacheMu.Unlock()
	next := make(map[lineCacheKey][]roomDisplayLine, len(messages))
	var lines []roomDisplayLine
	for _, msg := range messages {
		key := lineCacheKey{msg.Timestamp.UnixNano(), msg.Sender, msg.SenderID, msg.Text, msg.IsChat, msg.IsSelf, maxW, theme.ID, lang}
		ml, ok := r.lineCache[key]
		if !ok {
			ml = messageDisplayLines(msg, maxW, theme)
		}
		next[key] = ml
		lines = append(lines, ml...)
	}
	r.lineCache = next
	return lines
}

// messageDisplayLines wraps one message into screen lines of at most maxW cells.
func messageDisplayLines(msg RoomMessage, maxW int, theme ThemePalette) []roomDisplayLine {
	var lines []roomDisplayLine
	{
		tsStr := fmt.Sprintf("[%s] ", msg.Timestamp.Format("15:04:05"))
		tsLen := cell.StringWidth(tsStr)

		if msg.IsChat {
			var senderBadge string
			var senderStyle cell.Style
			if msg.IsSelf {
				senderBadge = T("You: ")
				senderStyle = cell.Style{
					Fg:       theme.Success,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				}
			} else {
				senderBadge = msg.Sender + ": "
				senderStyle = cell.Style{
					Fg:       theme.Accent,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				}
			}

			badgeLen := tsLen + cell.StringWidth(senderBadge)
			availFirst := maxW - badgeLen
			if availFirst < 10 {
				availFirst = 10
			}

			indentSpaces := strings.Repeat(" ", badgeLen)
			rawSpans := parseMessageSpans(msg.Text)

			var paragraphs [][]chatSpan
			var curPara []chatSpan
			for _, span := range rawSpans {
				parts := strings.Split(strings.ReplaceAll(span.Text, "\t", chatTab), "\n")
				for pIdx, part := range parts {
					if pIdx > 0 {
						paragraphs = append(paragraphs, curPara)
						curPara = nil
					}
					if part != "" || len(parts) == 1 {
						curPara = append(curPara, chatSpan{
							Text:     part,
							IsLink:   span.IsLink,
							ClickURL: span.ClickURL,
							IsCopy:   span.IsCopy,
							CopyText: span.CopyText,
						})
					}
				}
			}
			if len(curPara) > 0 || len(paragraphs) == 0 {
				paragraphs = append(paragraphs, curPara)
			}

			firstLineOverall := true
			for _, para := range paragraphs {
				wrappedLines, glue := wrapSpansToLines(para, availFirst)
				for wrapIdx, lSpans := range wrappedLines {
					isContinuation := (wrapIdx > 0)
					if firstLineOverall {
						lines = append(lines, roomDisplayLine{
							Timestamp:      tsStr,
							Badge:          senderBadge,
							BadgeStyle:     senderStyle,
							Spans:          lSpans,
							IsChat:         true,
							IsContinuation: false,
							RawMessage:     msg.Text,
						})
						firstLineOverall = false
					} else {
						lines = append(lines, roomDisplayLine{
							Timestamp:      indentSpaces,
							Spans:          lSpans,
							IsChat:         true,
							IsContinuation: isContinuation,
							Glue:           glue[wrapIdx],
							RawMessage:     msg.Text,
						})
					}
				}
			}
		} else {
			logColor := theme.TextMuted
			if strings.Contains(msg.Text, "[+]") || strings.Contains(msg.Text, "joined") {
				logColor = theme.Success
			} else if strings.Contains(msg.Text, "[-]") || strings.Contains(msg.Text, "left") {
				logColor = theme.Warning
			} else if strings.Contains(msg.Text, "[WARN]") || strings.Contains(msg.Text, "[ERROR]") || strings.Contains(msg.Text, "[SECURITY]") {
				logColor = theme.Danger
			}

			availFirst := maxW - tsLen
			if availFirst < 10 {
				availFirst = 10
			}

			logLines, glue := wrapWordsToLines(strings.ReplaceAll(tr(msg.Text), "\t", chatTab), availFirst)
			indentSpaces := strings.Repeat(" ", tsLen)

			for idx, lText := range logLines {
				if idx == 0 {
					lines = append(lines, roomDisplayLine{
						Timestamp:      tsStr,
						Text:           lText,
						TextStyle:      cell.Style{Fg: logColor, Bg: theme.SurfaceBg},
						IsChat:         false,
						IsContinuation: false,
						RawMessage:     msg.Text,
					})
				} else {
					lines = append(lines, roomDisplayLine{
						Timestamp:      indentSpaces,
						Text:           lText,
						TextStyle:      cell.Style{Fg: logColor, Bg: theme.SurfaceBg},
						IsChat:         false,
						IsContinuation: true,
						Glue:           glue[idx],
						RawMessage:     msg.Text,
					})
				}
			}
		}
	}
	return lines
}

// renderChatSpans draws spans from startX in at most maxW cells and returns the characters
// drawn and the column after them.
func (r *RoomView) renderChatSpans(frame *terminal.Frame, buf *buffer.Buffer, startX, rowY uint16, spans []chatSpan, maxW int) ([]renderedChatChar, uint16) {
	var chars []renderedChatChar
	if maxW <= 0 || len(spans) == 0 {
		return chars, startX
	}
	theme := CurrentTheme()
	plainStyle := cell.Style{
		Fg: theme.Text,
		Bg: theme.SurfaceBg,
	}
	linkStyle := cell.Style{
		Fg:       theme.Secondary,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierUnderline | cell.ModifierBold,
	}
	copyStyle := cell.Style{
		Fg:       theme.Warning,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierUnderline | cell.ModifierBold,
	}

	curX := startX
	endX := startX + uint16(maxW)

	for _, span := range spans {
		if curX >= endX {
			break
		}
		sRunes := []rune(span.Text)
		sRunes = sRunes[:fitRunesWithin(sRunes, int(endX-curX))]
		if len(sRunes) == 0 {
			continue
		}

		if span.IsCopy {
			buf.SetString(curX, rowY, string(sRunes), copyStyle)
		} else if span.IsLink {
			// Also an OSC 8 hyperlink where the terminal supports them: it opens the
			// address on the user's own machine, even through SSH.
			buf.SetString(curX, rowY, string(sRunes), linkStyle.WithLink(span.ClickURL))
		} else {
			buf.SetString(curX, rowY, string(sRunes), plainStyle)
		}

		for _, ru := range sRunes {
			if w := cell.RuneWidth(ru); w > 0 {
				chars = append(chars, renderedChatChar{X: curX, Y: rowY, R: ru})
				curX += uint16(w)
			}
		}
	}
	return chars, curX
}

func (r *RoomView) isCellSelected(x, y uint16) bool {
	sX, sY := r.SelectionStartX, r.SelectionStartY
	eX, eY := r.SelectionEndX, r.SelectionEndY

	if sY > eY || (sY == eY && sX > eX) {
		sX, eX = eX, sX
		sY, eY = eY, sY
	}

	iy := int(y)
	ix := int(x)

	if iy < sY || iy > eY {
		return false
	}
	if sY == eY {
		return ix >= sX && ix <= eX
	}
	if iy == sY {
		return ix >= sX
	}
	if iy == eY {
		return ix <= eX
	}
	return true
}

func (r *RoomView) extractSelectedText() string {
	if len(r.renderedLines) == 0 {
		return ""
	}
	sX, sY := r.SelectionStartX, r.SelectionStartY
	eX, eY := r.SelectionEndX, r.SelectionEndY

	if sY > eY || (sY == eY && sX > eX) {
		sX, eX = eX, sX
		sY, eY = eY, sY
	}

	var result strings.Builder
	firstLine := true

	for _, rl := range r.renderedLines {
		iy := int(rl.RowY)
		if iy < sY || iy > eY {
			continue
		}
		var lineStr strings.Builder
		for _, ch := range rl.Chars {
			ix := int(ch.X)
			var inSel bool
			if sY == eY {
				inSel = (ix >= sX && ix <= eX)
			} else if iy == sY {
				inSel = (ix >= sX)
			} else if iy == eY {
				inSel = (ix <= eX)
			} else {
				inSel = true
			}
			if inSel {
				lineStr.WriteRune(ch.R)
			}
		}
		extracted := lineStr.String()
		if extracted != "" {
			if firstLine {
				result.WriteString(extracted)
				firstLine = false
			} else {
				if rl.IsContinuation {
					// A wrapped line: no line break, and the whitespace the wrap took, if any.
					result.WriteString(rl.Glue)
					result.WriteString(extracted)
				} else {
					// Actual separate paragraph or message
					result.WriteRune('\n')
					result.WriteString(extracted)
				}
			}
		}
	}
	return SanitizeClipboardText(result.String())
}

func (r *RoomView) HandleChatClick(x, y uint16) bool {
	r.mu.Lock()
	var copyTextToUse, clickURLToUse string
	for _, rl := range r.renderedLines {
		if rl.RowY == y {
			if rl.CopyText != "" {
				copyTextToUse = rl.CopyText
				break
			} else if rl.ClickURL != "" {
				clickURLToUse = rl.ClickURL
				break
			} else if rl.RawMessage != "" {
				// A wrapped row of a [Copy: …] message has no button of its own. Plain
				// text is not copied on a click: drag over it to select and copy.
				copyTextToUse = messageCopyText(rl.RawMessage)
				break
			}
		}
	}
	r.mu.Unlock()

	if copyTextToUse != "" {
		r.ClearSelection()
		CopyToClipboard(copyTextToUse)
		r.SetToast(copiedToast) // the copied text itself stays out of the chat
		return true
	}
	if clickURLToUse != "" {
		r.ClearSelection()
		_ = OpenBrowserURL(clickURLToUse)
		CopyToClipboard(clickURLToUse)
		r.SetToast(fmt.Sprintf("🔗 Link opened: %s", clickURLToUse))
		return true
	}
	return false
}

func (r *RoomView) HandleMousePress(x, y uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SelectionDragging = true
	r.SelectionStartX = int(x)
	r.SelectionStartY = int(y)
	r.SelectionEndX = int(x)
	r.SelectionEndY = int(y)
	r.SelectionActive = false
	r.SelectedText = ""
}

func (r *RoomView) HandleMouseDrag(x, y uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.SelectionDragging {
		r.SelectionDragging = true
		r.SelectionStartX = int(x)
		r.SelectionStartY = int(y)
	}
	r.SelectionEndX = int(x)
	r.SelectionEndY = int(y)
	if r.SelectionStartX != r.SelectionEndX || r.SelectionStartY != r.SelectionEndY {
		r.SelectionActive = true
		r.SelectedText = r.extractSelectedText()
	}
}

func (r *RoomView) HandleMouseRelease(x, y uint16) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SelectionDragging = false
	if r.SelectionActive {
		r.SelectionEndX = int(x)
		r.SelectionEndY = int(y)
		if r.SelectionStartX != r.SelectionEndX || r.SelectionStartY != r.SelectionEndY {
			r.SelectedText = r.extractSelectedText()
			return r.SelectedText
		}
		r.SelectionActive = false
		r.SelectedText = ""
	}
	return ""
}

func (r *RoomView) ClearSelection() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SelectionActive = false
	r.SelectionDragging = false
	r.SelectedText = ""
}
