package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	filepath "path"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/honmaple/cloudfs/utils/pathutil"
	"github.com/honmaple/cloudfs/utils/structutil"
)

const defaultEndpoint = "https://graph.microsoft.com/v1.0"

type Option struct {
	AccessToken string        `json:"access_token" validate:"required"`
	Endpoint    string        `json:"endpoint"`
	DriveID     string        `json:"drive_id"`
	UserID      string        `json:"user_id"`
	RootID      string        `json:"root_id"`
	PageSize    int64         `json:"page_size"`
	CopyTimeout time.Duration `json:"copy_timeout"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type OneDrive struct {
	cloudfs.BaseFS
	opt      *Option
	client   *resty.Client
	endpoint string
	drive    string
	rootID   string
}

var _ cloudfs.FS = (*OneDrive)(nil)

func (d *OneDrive) itemURL(id string) string {
	if id == "" || id == "root" {
		return d.drive + "/root"
	}
	return d.drive + "/items/" + url.PathEscape(id)
}

func (d *OneDrive) request(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		path = d.endpoint + path
	}

	req := d.client.R().
		SetContext(ctx).
		SetDoNotParseResponse(true).
		SetHeader("Authorization", "Bearer "+d.opt.AccessToken)
	if body != nil {
		req.SetBody(body)
		req.SetHeader("Content-Type", "application/json")
	}
	if len(headers) > 0 {
		req.SetHeaders(headers)
	}

	resp, err := req.Execute(method, path)
	if err != nil {
		return nil, err
	}
	raw := resp.RawResponse
	if raw.StatusCode >= 200 && raw.StatusCode < 300 {
		return raw, nil
	}
	defer raw.Body.Close()
	data, _ := io.ReadAll(raw.Body)
	if raw.StatusCode == http.StatusNotFound {
		return nil, fs.ErrNotExist
	}
	if raw.StatusCode == http.StatusConflict {
		return nil, fs.ErrExist
	}
	return nil, fmt.Errorf("onedrive: %s: %s", raw.Status, strings.TrimSpace(string(data)))
}

func (d *OneDrive) requestJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	resp, err := d.request(ctx, method, path, body, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *OneDrive) getItem(ctx context.Context, id string) (*driveItem, error) {
	var item driveItem
	err := d.requestJSON(ctx, http.MethodGet, d.itemURL(id), nil, &item)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (d *OneDrive) listChildren(ctx context.Context, id string) ([]driveItem, error) {
	pageSize := d.opt.PageSize
	if pageSize <= 0 {
		pageSize = 200
	}

	path := d.itemURL(id) + "/children?$top=" + strconv.FormatInt(pageSize, 10)
	items := make([]driveItem, 0)
	for {
		var result listResponse
		if err := d.requestJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, err
		}
		items = append(items, result.Value...)
		if result.NextLink == "" {
			break
		}
		path = result.NextLink
	}
	return items, nil
}

func (d *OneDrive) findChild(ctx context.Context, parentID, name string) (*driveItem, error) {
	items, err := d.listChildren(ctx, parentID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, fs.ErrNotExist
}

func (d *OneDrive) resolve(ctx context.Context, path string) (*driveItem, error) {
	path = pathutil.CleanPath(path)
	if path == "/" {
		return d.getItem(ctx, d.rootID)
	}

	parentID := d.rootID
	var item *driveItem
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		var err error
		item, err = d.findChild(ctx, parentID, part)
		if err != nil {
			return nil, err
		}
		parentID = item.ID
	}
	return item, nil
}

func (d *OneDrive) resolveParent(ctx context.Context, path string) (*driveItem, string, error) {
	path = pathutil.CleanPath(path)
	if path == "/" {
		return nil, "", fs.ErrInvalid
	}
	parent, err := d.resolve(ctx, filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	if !parent.IsDir() {
		return nil, "", &fs.PathError{Op: "parent", Path: filepath.Dir(path), Err: errors.New("parent must be a dir")}
	}
	return parent, filepath.Base(path), nil
}

func (d *OneDrive) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	parent, err := d.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if !parent.IsDir() {
		return nil, &fs.PathError{Op: "list", Path: path, Err: errors.New("not a dir")}
	}
	items, err := d.listChildren(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	files := make([]cloudfs.FileInfo, len(items))
	for i := range items {
		files[i] = newFile(path, &items[i])
	}
	return files, nil
}

func (d *OneDrive) Move(ctx context.Context, src, dst string) error {
	srcItem, err := d.resolve(ctx, src)
	if err != nil {
		return err
	}
	dstItem, err := d.resolve(ctx, dst)
	if err != nil {
		return err
	}
	if !dstItem.IsDir() {
		return &fs.PathError{Op: "move", Path: dst, Err: errors.New("move dst must be a dir")}
	}
	body := map[string]any{
		"parentReference": map[string]string{"id": dstItem.ID},
	}
	return d.requestJSON(ctx, http.MethodPatch, d.itemURL(srcItem.ID), body, nil)
}

func (d *OneDrive) Copy(ctx context.Context, src, dst string) error {
	srcItem, err := d.resolve(ctx, src)
	if err != nil {
		return err
	}
	dstItem, err := d.resolve(ctx, dst)
	if err != nil {
		return err
	}
	if !dstItem.IsDir() {
		return &fs.PathError{Op: "copy", Path: dst, Err: errors.New("copy dst must be a dir")}
	}

	resp, err := d.request(ctx, http.MethodPost, d.itemURL(srcItem.ID)+"/copy", jsonBody(map[string]any{
		"name":            srcItem.Name,
		"parentReference": map[string]string{"id": dstItem.ID},
	}), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return nil
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return nil
	}
	return d.waitCopy(ctx, location)
}

func (d *OneDrive) Rename(ctx context.Context, path, newName string) error {
	item, err := d.resolve(ctx, path)
	if err != nil {
		return err
	}
	return d.requestJSON(ctx, http.MethodPatch, d.itemURL(item.ID), map[string]string{"name": newName}, nil)
}

func (d *OneDrive) Remove(ctx context.Context, path string) error {
	item, err := d.resolve(ctx, path)
	if err != nil {
		return err
	}
	return d.requestJSON(ctx, http.MethodDelete, d.itemURL(item.ID), nil, nil)
}

func (d *OneDrive) MakeDir(ctx context.Context, path string) error {
	parent, name, err := d.resolveParent(ctx, path)
	if err != nil {
		return err
	}
	item, err := d.findChild(ctx, parent.ID, name)
	if err == nil {
		if item.IsDir() {
			return nil
		}
		return fs.ErrExist
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	body := map[string]any{
		"name":                              name,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "fail",
	}
	return d.requestJSON(ctx, http.MethodPost, d.itemURL(parent.ID)+"/children", body, nil)
}

func (d *OneDrive) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	item, err := d.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	return newFile(filepath.Dir(pathutil.CleanPath(path)), item), nil
}

func (d *OneDrive) Open(ctx context.Context, path string) (cloudfs.File, error) {
	item, err := d.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if item.IsDir() {
		return nil, cloudfs.ErrOpenDirectory
	}
	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		headers := map[string]string{}
		if length > 0 {
			headers["Range"] = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
		} else {
			headers["Range"] = fmt.Sprintf("bytes=%d-", offset)
		}
		resp, err := d.request(ctx, http.MethodGet, d.itemURL(item.ID)+"/content", nil, headers)
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
	return cloudfs.NewFile(item.Size, rangeFunc)
}

func (d *OneDrive) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	parent, name, err := d.resolveParent(ctx, path)
	if err != nil {
		return nil, err
	}
	r, w := ioutil.Pipe()
	go func() {
		uploadPath := d.itemURL(parent.ID) + ":/" + url.PathEscape(name) + ":/content"
		resp, err := d.request(ctx, http.MethodPut, uploadPath, r, map[string]string{
			"Content-Type": "application/octet-stream",
		})
		if err != nil {
			r.CloseWithError(err)
			return
		}
		resp.Body.Close()
		r.Close()
	}()
	return w, nil
}

func (d *OneDrive) Close() error {
	return nil
}

func (d *OneDrive) waitCopy(ctx context.Context, location string) error {
	timeout := d.opt.CopyTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		resp, err := d.request(ctx, http.MethodGet, location, nil, nil)
		if err != nil {
			return err
		}
		var status asyncStatus
		if resp.Body != nil {
			_ = json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent || status.Status == "completed" {
			return nil
		}
		if status.Status == "failed" || status.Status == "deleteFailed" {
			return fmt.Errorf("onedrive copy failed: %s", status.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func jsonBody(value any) io.Reader {
	data, _ := json.Marshal(value)
	return bytes.NewReader(data)
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(opt.Endpoint, "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	drive := "/me/drive"
	if opt.DriveID != "" {
		drive = "/drives/" + url.PathEscape(opt.DriveID)
	} else if opt.UserID != "" {
		drive = "/users/" + url.PathEscape(opt.UserID) + "/drive"
	}
	rootID := opt.RootID
	if rootID == "" {
		rootID = "root"
	}
	return &OneDrive{
		opt:      opt,
		client:   resty.New(),
		endpoint: endpoint,
		drive:    drive,
		rootID:   rootID,
	}, nil
}

func init() {
	cloudfs.Register("onedrive", func() cloudfs.Option {
		return &Option{}
	})
}
