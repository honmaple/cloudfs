package gdrive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	filepath "path"
	"strings"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/honmaple/cloudfs/utils/pathutil"
	"github.com/honmaple/cloudfs/utils/structutil"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	folderMimeType = "application/vnd.google-apps.folder"
	googleMimeType = "application/vnd.google-apps."
)

type Option struct {
	Credentials       string `json:"credentials"`
	CredentialsFile   string `json:"credentials_file"`
	AccessToken       string `json:"access_token"`
	RootID            string `json:"root_id"`
	SharedDriveID     string `json:"shared_drive_id"`
	ExportMimeType    string `json:"export_mime_type"`
	PageSize          int64  `json:"page_size"`
	AcknowledgeAbuse  bool   `json:"acknowledge_abuse"`
	SupportsAllDrives bool   `json:"supports_all_drives"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type GoogleDrive struct {
	cloudfs.BaseFS
	opt    *Option
	client *drive.Service
	rootID string
}

var _ cloudfs.FS = (*GoogleDrive)(nil)

func (d *GoogleDrive) supportsAllDrives() bool {
	return d.opt.SupportsAllDrives || d.opt.SharedDriveID != ""
}

func (d *GoogleDrive) getFile(ctx context.Context, id string) (*drive.File, error) {
	call := d.client.Files.Get(id).
		Fields("id", "name", "mimeType", "size", "modifiedTime", "parents")
	if d.supportsAllDrives() {
		call.SupportsAllDrives(true)
	}
	return call.Context(ctx).Do()
}

func (d *GoogleDrive) listFiles(ctx context.Context, q string) ([]*drive.File, error) {
	pageSize := d.opt.PageSize
	if pageSize <= 0 {
		pageSize = 1000
	}

	files := make([]*drive.File, 0)
	call := d.client.Files.List().
		Q(q).
		PageSize(pageSize).
		Fields("nextPageToken", "files(id,name,mimeType,size,modifiedTime,parents)").
		OrderBy("folder,name_natural")
	if d.supportsAllDrives() {
		call.SupportsAllDrives(true).IncludeItemsFromAllDrives(true)
	}
	if d.opt.SharedDriveID != "" {
		call.Corpora("drive").DriveId(d.opt.SharedDriveID)
	}

	for {
		result, err := call.Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		files = append(files, result.Files...)
		if result.NextPageToken == "" {
			break
		}
		call.PageToken(result.NextPageToken)
	}
	return files, nil
}

func (d *GoogleDrive) findChild(ctx context.Context, parentID, name string) (*drive.File, error) {
	q := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false", escapeQuery(parentID), escapeQuery(name))
	files, err := d.listFiles(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fs.ErrNotExist
	}
	return files[0], nil
}

func (d *GoogleDrive) resolve(ctx context.Context, path string) (*drive.File, error) {
	path = pathutil.CleanPath(path)
	if path == "/" {
		return d.getFile(ctx, d.rootID)
	}

	parentID := d.rootID
	var file *drive.File
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		var err error
		file, err = d.findChild(ctx, parentID, part)
		if err != nil {
			return nil, err
		}
		parentID = file.Id
	}
	return file, nil
}

func (d *GoogleDrive) resolveParent(ctx context.Context, path string) (*drive.File, string, error) {
	path = pathutil.CleanPath(path)
	if path == "/" {
		return nil, "", fs.ErrInvalid
	}
	parent, err := d.resolve(ctx, filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	if !isDir(parent) {
		return nil, "", &fs.PathError{Op: "parent", Path: filepath.Dir(path), Err: errors.New("parent must be a dir")}
	}
	return parent, filepath.Base(path), nil
}

func (d *GoogleDrive) createFile(ctx context.Context, info *drive.File, r io.Reader, mimeType string) (*drive.File, error) {
	call := d.client.Files.Create(info).
		Fields("id", "name", "mimeType", "size", "modifiedTime", "parents")
	if r != nil {
		if mimeType != "" {
			call.Media(r, googleapi.ContentType(mimeType))
		} else {
			call.Media(r)
		}
	}
	if d.supportsAllDrives() {
		call.SupportsAllDrives(true)
	}
	return call.Context(ctx).Do()
}

func (d *GoogleDrive) updateFile(ctx context.Context, id string, info *drive.File) (*drive.File, error) {
	call := d.client.Files.Update(id, info).
		Fields("id", "name", "mimeType", "size", "modifiedTime", "parents")
	if d.supportsAllDrives() {
		call.SupportsAllDrives(true)
	}
	return call.Context(ctx).Do()
}

func (d *GoogleDrive) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.File, error) {
	parent, err := d.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if !isDir(parent) {
		return nil, &fs.PathError{Op: "list", Path: path, Err: errors.New("not a dir")}
	}

	q := fmt.Sprintf("'%s' in parents and trashed = false", escapeQuery(parent.Id))
	files, err := d.listFiles(ctx, q)
	if err != nil {
		return nil, err
	}

	result := make([]cloudfs.File, len(files))
	for i, file := range files {
		result[i] = newFile(path, file)
	}
	return result, nil
}

func (d *GoogleDrive) Move(ctx context.Context, src, dst string) error {
	srcFile, err := d.resolve(ctx, src)
	if err != nil {
		return err
	}
	dstFile, err := d.resolve(ctx, dst)
	if err != nil {
		return err
	}
	if !isDir(dstFile) {
		return &fs.PathError{Op: "move", Path: dst, Err: errors.New("move dst must be a dir")}
	}

	call := d.client.Files.Update(srcFile.Id, &drive.File{}).
		AddParents(dstFile.Id).
		RemoveParents(strings.Join(srcFile.Parents, ",")).
		Fields("id")
	if d.supportsAllDrives() {
		call.SupportsAllDrives(true)
	}
	_, err = call.Context(ctx).Do()
	return err
}

func (d *GoogleDrive) Copy(ctx context.Context, src, dst string) error {
	srcFile, err := d.resolve(ctx, src)
	if err != nil {
		return err
	}
	dstFile, err := d.resolve(ctx, dst)
	if err != nil {
		return err
	}
	if !isDir(dstFile) {
		return &fs.PathError{Op: "copy", Path: dst, Err: errors.New("copy dst must be a dir")}
	}
	if isDir(srcFile) {
		return cloudfs.CopyDir(ctx, d, src, filepath.Join(dst, filepath.Base(src)))
	}

	call := d.client.Files.Copy(srcFile.Id, &drive.File{
		Name:    srcFile.Name,
		Parents: []string{dstFile.Id},
	}).Fields("id")
	if d.supportsAllDrives() {
		call.SupportsAllDrives(true)
	}
	_, err = call.Context(ctx).Do()
	return err
}

func (d *GoogleDrive) Rename(ctx context.Context, path, newName string) error {
	file, err := d.resolve(ctx, path)
	if err != nil {
		return err
	}
	_, err = d.updateFile(ctx, file.Id, &drive.File{Name: newName})
	return err
}

func (d *GoogleDrive) Remove(ctx context.Context, path string) error {
	file, err := d.resolve(ctx, path)
	if err != nil {
		return err
	}
	call := d.client.Files.Delete(file.Id)
	if d.supportsAllDrives() {
		call.SupportsAllDrives(true)
	}
	return call.Context(ctx).Do()
}

func (d *GoogleDrive) MakeDir(ctx context.Context, path string) error {
	parent, name, err := d.resolveParent(ctx, path)
	if err != nil {
		return err
	}
	file, err := d.findChild(ctx, parent.Id, name)
	if err == nil {
		if isDir(file) {
			return nil
		}
		return fs.ErrExist
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, err = d.createFile(ctx, &drive.File{
		Name:     name,
		MimeType: folderMimeType,
		Parents:  []string{parent.Id},
	}, nil, "")
	return err
}

func (d *GoogleDrive) Get(ctx context.Context, path string) (cloudfs.File, error) {
	file, err := d.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	return newFile(filepath.Dir(pathutil.CleanPath(path)), file), nil
}

func (d *GoogleDrive) Open(path string) (cloudfs.FileReader, error) {
	ctx := context.Background()
	file, err := d.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if isDir(file) {
		return nil, cloudfs.ErrOpenDirectory
	}

	size := file.Size
	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		if isGoogleApp(file) {
			return d.export(ctx, file.Id, offset)
		}
		call := d.client.Files.Get(file.Id)
		if d.supportsAllDrives() {
			call.SupportsAllDrives(true)
		}
		if d.opt.AcknowledgeAbuse {
			call.AcknowledgeAbuse(true)
		}
		if length > 0 {
			call.Header().Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
		} else {
			call.Header().Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := call.Context(ctx).Download()
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
	return cloudfs.NewFileReader(size, rangeFunc)
}

func (d *GoogleDrive) Create(path string) (cloudfs.FileWriter, error) {
	parent, name, err := d.resolveParent(context.Background(), path)
	if err != nil {
		return nil, err
	}

	r, w := ioutil.Pipe()
	go func() {
		ctx := context.Background()
		file, err := d.findChild(ctx, parent.Id, name)
		if err == nil {
			if isDir(file) {
				err = cloudfs.ErrOpenDirectory
			} else {
				call := d.client.Files.Update(file.Id, &drive.File{}).Media(r).Fields("id")
				if d.supportsAllDrives() {
					call.SupportsAllDrives(true)
				}
				_, err = call.Context(ctx).Do()
			}
		} else if errors.Is(err, fs.ErrNotExist) {
			_, err = d.createFile(ctx, &drive.File{
				Name:    name,
				Parents: []string{parent.Id},
			}, r, "")
		}
		r.CloseWithError(err)
	}()
	return w, nil
}

func (d *GoogleDrive) Close() error {
	return nil
}

func (d *GoogleDrive) export(ctx context.Context, id string, offset int64) (io.ReadCloser, error) {
	if d.opt.ExportMimeType == "" {
		return nil, errors.New("google workspace file requires export_mime_type")
	}
	resp, err := d.client.Files.Export(id, d.opt.ExportMimeType).Context(ctx).Download()
	if err != nil {
		return nil, err
	}
	if offset <= 0 {
		return resp.Body, nil
	}
	if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}

	ctx := context.Background()
	clientOptions := []option.ClientOption{option.WithScopes(drive.DriveScope)}
	switch {
	case opt.Credentials != "":
		clientOptions = append(clientOptions, option.WithCredentialsJSON([]byte(opt.Credentials)))
	case opt.CredentialsFile != "":
		clientOptions = append(clientOptions, option.WithCredentialsFile(opt.CredentialsFile))
	case opt.AccessToken != "":
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: opt.AccessToken})
		clientOptions = append(clientOptions, option.WithHTTPClient(oauth2.NewClient(ctx, ts)))
	}

	client, err := drive.NewService(ctx, clientOptions...)
	if err != nil {
		return nil, err
	}

	rootID := opt.RootID
	if rootID == "" {
		rootID = "root"
	}
	if opt.SharedDriveID != "" && opt.RootID == "" {
		rootID = opt.SharedDriveID
	}

	return &GoogleDrive{
		opt:    opt,
		client: client,
		rootID: rootID,
	}, nil
}

func init() {
	cloudfs.Register("gdrive", func() cloudfs.Option {
		return &Option{}
	})
}
