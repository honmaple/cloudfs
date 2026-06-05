package onedrive

import (
	"io/fs"
	"time"

	"github.com/honmaple/cloudfs"
)

type driveItem struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Size            int64          `json:"size"`
	MimeType        string         `json:"mimeType"`
	ETag            string         `json:"eTag"`
	CTag            string         `json:"cTag"`
	WebURL          string         `json:"webUrl"`
	DownloadURL     string         `json:"@microsoft.graph.downloadUrl"`
	CreatedDateTime string         `json:"createdDateTime"`
	ModifiedTime    string         `json:"lastModifiedDateTime"`
	ParentReference map[string]any `json:"parentReference"`
	File            map[string]any `json:"file"`
	Folder          map[string]any `json:"folder"`
	Package         map[string]any `json:"package"`
}

func (d *driveItem) IsDir() bool {
	return d != nil && d.Folder != nil
}

type listResponse struct {
	Value    []driveItem `json:"value"`
	NextLink string      `json:"@odata.nextLink"`
}

type asyncStatus struct {
	Status string `json:"status"`
}

type fileinfo struct {
	info *driveItem
}

func (f *fileinfo) Name() string       { return f.info.Name }
func (f *fileinfo) Size() int64        { return f.info.Size }
func (f *fileinfo) Mode() fs.FileMode  { return 0 }
func (f *fileinfo) ModTime() time.Time { return parseTime(f.info.ModifiedTime) }
func (f *fileinfo) IsDir() bool        { return f.info.IsDir() }
func (f *fileinfo) Sys() any           { return f.info }

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

func newFile(path string, info *driveItem) cloudfs.File {
	return cloudfs.NewFile(path, &fileinfo{info: info}, func(fi *cloudfs.FileInfo) {
		fi.Type = info.MimeType
		if fi.Type == "" && info.IsDir() {
			fi.Type = "DIR"
		}
		fi.ExtraInfo = map[string]any{
			"id":               info.ID,
			"etag":             info.ETag,
			"ctag":             info.CTag,
			"web_url":          info.WebURL,
			"download_url":     info.DownloadURL,
			"parent_reference": info.ParentReference,
		}
	})
}
