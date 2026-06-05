package gdrive

import (
	"io/fs"
	"strings"
	"time"

	"github.com/honmaple/cloudfs"
	"google.golang.org/api/drive/v3"
)

type fileinfo struct {
	info *drive.File
}

func (f *fileinfo) Name() string       { return f.info.Name }
func (f *fileinfo) Size() int64        { return f.info.Size }
func (f *fileinfo) Mode() fs.FileMode  { return 0 }
func (f *fileinfo) ModTime() time.Time { return parseTime(f.info.ModifiedTime) }
func (f *fileinfo) IsDir() bool        { return isDir(f.info) }
func (f *fileinfo) Sys() any           { return f.info }

func isDir(file *drive.File) bool {
	return file != nil && file.MimeType == folderMimeType
}

func isGoogleApp(file *drive.File) bool {
	return file != nil && strings.HasPrefix(file.MimeType, googleMimeType)
}

func escapeQuery(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `'`, `\'`)
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func newFile(path string, info *drive.File) cloudfs.FileInfo {
	return cloudfs.NewFileInfo(&fileinfo{info: info},
		func(entry *cloudfs.Entry) {
			entry.Path = path
			entry.Type = info.MimeType
			entry.ExtraInfo = map[string]any{
				"id":        info.Id,
				"mime_type": info.MimeType,
				"parents":   info.Parents,
			}
		},
	)
}
