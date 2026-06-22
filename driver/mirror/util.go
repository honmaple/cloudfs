package mirror

import (
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/honmaple/cloudfs"
)

func parseNginx(path string, doc *goquery.Document) []cloudfs.FileInfo {
	files := make([]cloudfs.FileInfo, 0)

	doc.Find("pre a").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			return
		}
		name := s.Text()

		entry := &cloudfs.Entry{
			Path:  path,
			Name:  strings.TrimSuffix(name, "/"),
			IsDir: strings.HasSuffix(name, "/"),
		}
		if len(s.Nodes) > 0 && s.Nodes[0].NextSibling != nil {
			if fields := strings.Fields(strings.TrimSpace(s.Nodes[0].NextSibling.Data)); len(fields) > 0 {
				if size, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
					entry.Size = int64(size)
				}
				if modTime, err := time.Parse("02-Jan-2006 15:04", strings.Join(fields[0:len(fields)-1], " ")); err == nil {
					entry.ModTime = modTime
				}
			}
		}
		files = append(files, entry.FileInfo())
	})
	return files
}

func parseAliyun(path string, doc *goquery.Document) []cloudfs.FileInfo {
	files := make([]cloudfs.FileInfo, 0)

	doc.Find("tbody tr").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			return
		}
		name := s.Find(".link a").Text()

		entry := &cloudfs.Entry{
			Path:  path,
			Name:  strings.TrimSuffix(name, "/"),
			IsDir: strings.HasSuffix(name, "/"),
		}

		if modTime, err := time.Parse("2006-01-02 15:04", strings.TrimSpace(s.Find(".date").Text())); err == nil {
			entry.ModTime = modTime
		}
		if parts := strings.Fields(s.Find(".size").Text()); len(parts) == 2 {
			size, err := strconv.ParseFloat(parts[0], 64)
			if err == nil {
				switch parts[1] {
				case "B":
					entry.Size = int64(size)
				case "KB":
					entry.Size = int64(size * 1024)
				case "MB":
					entry.Size = int64(size * 1024 * 1024)
				case "GB":
					entry.Size = int64(size * 1024 * 1024 * 1024)
				}
			}
		}
		files = append(files, entry.FileInfo())
	})
	return files
}

func parseTuna(path string, doc *goquery.Document) []cloudfs.FileInfo {
	files := make([]cloudfs.FileInfo, 0)

	doc.Find("tbody tr").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			return
		}
		name := s.Find(".link a").Text()

		entry := &cloudfs.Entry{
			Path:  path,
			Name:  strings.TrimSuffix(name, "/"),
			IsDir: strings.HasSuffix(name, "/"),
		}

		if modTime, err := time.Parse("2006-01-02 15:04", strings.TrimSpace(s.Find(".date").Text())); err == nil {
			entry.ModTime = modTime
		}
		if parts := strings.Fields(s.Find(".size").Text()); len(parts) == 2 {
			size, err := strconv.ParseFloat(parts[0], 64)
			if err == nil {
				switch parts[1] {
				case "B":
					entry.Size = int64(size)
				case "KiB":
					entry.Size = int64(size * 1024)
				case "MiB":
					entry.Size = int64(size * 1024 * 1024)
				case "GiB":
					entry.Size = int64(size * 1024 * 1024 * 1024)
				}
			}
		}
		files = append(files, entry.FileInfo())
	})
	return files
}
