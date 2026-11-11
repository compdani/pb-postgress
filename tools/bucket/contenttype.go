package bucket

import (
	"net/http"
	"path/filepath"
	"strings"
)

// extensionTypes overrides sniffing for types PgBase serves explicitly.
var extensionTypes = map[string]string{
	".svg":  "image/svg+xml",
	".css":  "text/css",
	".js":   "text/javascript",
	".mjs":  "text/javascript",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".json": "application/json",
	".txt":  "text/plain; charset=utf-8",
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".pdf":  "application/pdf",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
}

var inlineTypes = map[string]struct{}{
	"image/png":       {},
	"image/jpeg":      {},
	"image/gif":       {},
	"image/webp":      {},
	"image/bmp":       {},
	"video/webm":      {},
	"video/mp4":       {},
	"audio/mpeg":      {},
	"audio/wav":       {},
	"audio/mp3":       {},
	"application/pdf": {},
}

func contentType(name string, sniff []byte, declared string) string {
	if ct, ok := extensionTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return ct
	}
	detected := http.DetectContentType(sniff)
	if detected != "" && detected != "application/octet-stream" {
		return detected
	}
	declared = strings.TrimSpace(declared)
	if i := strings.IndexByte(declared, ';'); i >= 0 {
		declared = strings.TrimSpace(declared[:i])
	}
	if validMIME(declared) {
		return declared
	}
	if detected != "" {
		return detected
	}
	return "application/octet-stream"
}

func validMIME(s string) bool {
	if s == "" || len(s) > 127 || strings.ContainsAny(s, " \r\n\t") {
		return false
	}
	left, right, ok := strings.Cut(s, "/")
	return ok && left != "" && right != "" && !strings.Contains(right, "/")
}

func inlineContent(contentType string) bool {
	base := contentType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	_, ok := inlineTypes[strings.ToLower(base)]
	return ok
}
