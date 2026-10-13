package remoteapi

import "strings"

// ForwardedRecord is a single forwarded bundle. Names and times inside it are
// untrusted quoted labels, never identity or event timestamp authority.
type ForwardedRecord struct {
	Title string
	Items []RecordItem
}

type RecordItem struct {
	SenderName  string
	Time        string
	Text        string
	Media       *Media
	MediaStatus string // Readable reason when media cannot be retrieved.
	Record      *ForwardedRecord
}

func (r *ForwardedRecord) PlainText() string {
	if r == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("[合并聊天记录] " + r.Title)
	for _, item := range r.Items {
		out.WriteString("\n\n")
		out.WriteString(strings.TrimSpace(item.SenderName + " " + item.Time))
		out.WriteString("\n" + item.Text)
		if item.MediaStatus != "" {
			out.WriteString("（" + item.MediaStatus + "）")
		}
		if item.Record != nil {
			out.WriteString("\n" + strings.ReplaceAll(item.Record.PlainText(), "\n", "\n  "))
		}
	}
	return out.String()
}
