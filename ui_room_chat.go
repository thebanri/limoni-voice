// Room view: chat message parsing, wrapping, text selection and mouse handling.

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

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
	RawMessage     string
}

var (
	reChatURL   = regexp.MustCompile(`https?://[^\s<>"]+|www\.[^\s<>"]+`)
	reChatCopy1 = regexp.MustCompile(`(?s)📋\s*\[(?:Kopyala|Copy|COPY):\s*([^\]]+)\]`)
	reChatCopy2 = regexp.MustCompile(`(?s)\[(?:kopyala|copy|KOPYALA|COPY|Kopyala|Copy):\s*([^\]]+)\]`)
	reChatCopy3 = regexp.MustCompile(`copy://([^\s<>"]+)`)
)

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

func wrapSpansToLines(spans []chatSpan, availWidth int) [][]chatSpan {
	if availWidth < 5 {
		availWidth = 5
	}

	var lines [][]chatSpan
	var currentLine []chatSpan
	currentWidth := 0

	flushLine := func() {
		if len(currentLine) > 0 {
			lines = append(lines, currentLine)
			currentLine = nil
			currentWidth = 0
		}
	}

	appendSpan := func(span chatSpan, width int) {
		if len(currentLine) > 0 && !currentLine[len(currentLine)-1].IsLink && !currentLine[len(currentLine)-1].IsCopy && !span.IsLink && !span.IsCopy {
			currentLine[len(currentLine)-1].Text += span.Text
		} else {
			currentLine = append(currentLine, span)
		}
		currentWidth += width
	}

	for _, span := range spans {
		if !span.IsLink && !span.IsCopy {
			tokens := splitWordsAndSpaces(span.Text)
			for _, tok := range tokens {
				tRunes := []rune(tok)
				tLen := len(tRunes)

				if unicode.IsSpace(tRunes[0]) && currentWidth == 0 {
					continue
				}

				if currentWidth+tLen <= availWidth {
					appendSpan(chatSpan{Text: tok, IsLink: false, IsCopy: false}, tLen)
				} else {
					if currentWidth > 0 {
						flushLine()
					}
					if unicode.IsSpace(tRunes[0]) {
						continue
					}
					if tLen <= availWidth {
						appendSpan(chatSpan{Text: tok, IsLink: false, IsCopy: false}, tLen)
					} else {
						for len(tRunes) > 0 {
							chunkLen := min(len(tRunes), availWidth)
							chunkStr := string(tRunes[:chunkLen])
							if len(tRunes) > availWidth {
								lines = append(lines, []chatSpan{{Text: chunkStr, IsLink: false, IsCopy: false}})
								tRunes = tRunes[chunkLen:]
							} else {
								appendSpan(chatSpan{Text: chunkStr, IsLink: false, IsCopy: false}, chunkLen)
								tRunes = tRunes[chunkLen:]
							}
						}
					}
				}
			}
		} else {
			// Interactive span (Link or Copy)
			lRunes := []rune(span.Text)
			lLen := len(lRunes)

			if currentWidth+lLen <= availWidth {
				appendSpan(span, lLen)
			} else {
				if currentWidth > 0 {
					flushLine()
				}
				if lLen <= availWidth {
					appendSpan(span, lLen)
				} else {
					for len(lRunes) > 0 {
						chunkLen := min(len(lRunes), availWidth)
						chunkStr := string(lRunes[:chunkLen])
						newSpan := chatSpan{
							Text:     chunkStr,
							IsLink:   span.IsLink,
							ClickURL: span.ClickURL,
							IsCopy:   span.IsCopy,
							CopyText: span.CopyText,
						}
						if len(lRunes) > availWidth {
							lines = append(lines, []chatSpan{newSpan})
							lRunes = lRunes[chunkLen:]
						} else {
							appendSpan(newSpan, chunkLen)
							lRunes = lRunes[chunkLen:]
						}
					}
				}
			}
		}
	}

	flushLine()
	if len(lines) == 0 {
		lines = append(lines, []chatSpan{{Text: "", IsLink: false, IsCopy: false}})
	}
	return lines
}

func wrapWordsToLines(text string, maxW int) []string {
	if maxW < 5 {
		maxW = 5
	}
	tokens := splitWordsAndSpaces(text)
	var lines []string
	var cur strings.Builder
	curLen := 0

	flush := func() {
		if cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curLen = 0
		}
	}

	for _, tok := range tokens {
		tRunes := []rune(tok)
		tLen := len(tRunes)
		if unicode.IsSpace(tRunes[0]) && curLen == 0 {
			continue
		}
		if curLen+tLen <= maxW {
			cur.WriteString(tok)
			curLen += tLen
		} else {
			if curLen > 0 {
				flush()
			}
			if unicode.IsSpace(tRunes[0]) {
				continue
			}
			if tLen <= maxW {
				cur.WriteString(tok)
				curLen = tLen
			} else {
				for len(tRunes) > 0 {
					cLen := min(len(tRunes), maxW)
					if len(tRunes) > maxW {
						lines = append(lines, string(tRunes[:cLen]))
						tRunes = tRunes[cLen:]
					} else {
						cur.WriteString(string(tRunes[:cLen]))
						curLen = cLen
						tRunes = tRunes[cLen:]
					}
				}
			}
		}
	}
	flush()
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

func (r *RoomView) buildDisplayLines(messages []RoomMessage, maxW int) []roomDisplayLine {
	var lines []roomDisplayLine
	if maxW < 10 {
		maxW = 10
	}
	theme := CurrentTheme()

	for _, msg := range messages {
		tsStr := fmt.Sprintf("[%s] ", msg.Timestamp.Format("15:04:05"))
		tsLen := len([]rune(tsStr))

		if msg.IsChat {
			var senderBadge string
			var senderStyle cell.Style
			if msg.IsSelf {
				senderBadge = "You: "
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

			badgeLen := tsLen + len([]rune(senderBadge))
			availFirst := maxW - badgeLen
			if availFirst < 10 {
				availFirst = 10
			}

			indentSpaces := strings.Repeat(" ", badgeLen)
			rawSpans := parseMessageSpans(msg.Text)

			var paragraphs [][]chatSpan
			var curPara []chatSpan
			for _, span := range rawSpans {
				parts := strings.Split(span.Text, "\n")
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
				wrappedLines := wrapSpansToLines(para, availFirst)
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

			logLines := wrapWordsToLines(msg.Text, availFirst)
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
						RawMessage:     msg.Text,
					})
				}
			}
		}
	}

	return lines
}

func (r *RoomView) renderChatSpans(frame *terminal.Frame, buf *buffer.Buffer, startX, rowY uint16, spans []chatSpan, maxW int) []renderedChatChar {
	var chars []renderedChatChar
	if maxW <= 0 || len(spans) == 0 {
		return chars
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
		rem := int(endX - curX)
		drawnLen := len(sRunes)
		if drawnLen > rem {
			sRunes = sRunes[:rem]
			drawnLen = rem
		}
		if drawnLen <= 0 {
			continue
		}

		if span.IsCopy {
			buf.SetString(curX, rowY, string(sRunes), copyStyle)
		} else if span.IsLink {
			buf.SetString(curX, rowY, string(sRunes), linkStyle)
		} else {
			buf.SetString(curX, rowY, string(sRunes), plainStyle)
		}

		col := curX
		for _, ru := range sRunes {
			w := cell.RuneWidth(ru)
			if w > 0 {
				chars = append(chars, renderedChatChar{X: col, Y: rowY, R: ru})
				col += uint16(w)
			}
		}

		curX += uint16(drawnLen)
	}
	return chars
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
					// Soft wrap line continuation (window resize wrap): do NOT insert fake \n!
					if !strings.HasSuffix(result.String(), " ") && !strings.HasPrefix(extracted, " ") {
						result.WriteRune(' ')
					}
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
				copyTextToUse = rl.RawMessage
				if m := reChatCopy1.FindStringSubmatch(copyTextToUse); len(m) > 1 {
					copyTextToUse = m[1]
				} else if m := reChatCopy2.FindStringSubmatch(copyTextToUse); len(m) > 1 {
					copyTextToUse = m[1]
				} else if m := reChatCopy3.FindStringSubmatch(copyTextToUse); len(m) > 1 {
					copyTextToUse = m[1]
				}
				break
			}
		}
	}
	r.mu.Unlock()

	if copyTextToUse != "" {
		r.ClearSelection()
		CopyToClipboard(copyTextToUse)
		previewStr := copyTextToUse
		if len([]rune(previewStr)) > 35 {
			previewStr = string([]rune(previewStr)[:35]) + "…"
		}
		r.SetToast(fmt.Sprintf("📋 Copied: %s", previewStr))
		r.AddLog(fmt.Sprintf("[CLIPBOARD] Copied: %s", previewStr))
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
