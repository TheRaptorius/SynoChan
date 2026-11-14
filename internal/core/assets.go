package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"synochan/internal/config"
	"synochan/internal/fourchan"
	"synochan/internal/queue"
)

// AssetType tells uploads and thumbnails apart.
type AssetType string

const (
	AssetUpload    AssetType = "Upload"
	AssetThumbnail AssetType = "Thumbnail"
)

// AssetID identifies a file of a thread. Its string form is "Type:Identifier".
type AssetID struct {
	Type AssetType
	ID   string
}

func (a AssetID) String() string { return string(a.Type) + ":" + a.ID }

// ParseAssetID parses the string form of an AssetID.
func ParseAssetID(s string) (AssetID, error) {
	typ, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return AssetID{}, fmt.Errorf("invalid asset id %q", s)
	}
	switch AssetType(typ) {
	case AssetUpload, AssetThumbnail:
		return AssetID{Type: AssetType(typ), ID: id}, nil
	}
	return AssetID{}, fmt.Errorf("invalid asset type in %q", s)
}

func uploadID(threadID int64, tim string) AssetID {
	return AssetID{Type: AssetUpload, ID: fmt.Sprintf("%d.%s", threadID, strings.ReplaceAll(tim, ":", "_"))}
}

func thumbnailID(site string, threadID, postNo int64) AssetID {
	return AssetID{Type: AssetThumbnail, ID: fmt.Sprintf("%s.%d.%d", site, threadID, postNo)}
}

// uploadFilename is the name an upload is saved under, by the configured format.
func uploadFilename(u fourchan.UploadInfo, format config.ImageFilenameFormat) string {
	original := u.UploadedFilename
	if original == "" {
		original = u.Tim
	}
	var name string
	switch format {
	case config.FilenameOriginal:
		name = original + u.Ext
	case config.FilenameIDAndOriginal:
		name = fmt.Sprintf("%d - %s%s", u.No, original, u.Ext)
	case config.FilenameOriginalAndID:
		name = fmt.Sprintf("%s - %d%s", original, u.No, u.Ext)
	default:
		name = fmt.Sprintf("%d%s", u.No, u.Ext)
	}
	return fourchan.ClampName(name)
}

// uploadItem downloads one uploaded file.
type uploadItem struct {
	t    *Thread
	info fourchan.UploadInfo
	id   AssetID
}

func (u *uploadItem) Context() context.Context { return u.t.ctx }
func (u *uploadItem) ShouldProcess() bool      { return u.t.ShouldProcess() }
func (u *uploadItem) ReadyAt() time.Time       { return time.Time{} }
func (u *uploadItem) Order() queue.Order       { return u.t.assetOrder(queue.Default) }
func (u *uploadItem) GaveUp()                  { u.t.markFailed(u.id) }
func (u *uploadItem) IsAsset() bool            { return true }
func (u *uploadItem) String() string           { return "Upload " + u.id.ID }

func (u *uploadItem) Process(ctx context.Context) (queue.Result, error) {
	if !u.ShouldProcess() {
		return queue.Result{Remove: true}, nil
	}
	u.t.active.Add(1)
	defer u.t.active.Done()
	dir, err := u.t.ensureDir()
	if err != nil {
		u.t.noteError(err)
		return queue.Result{}, err
	}
	name := uploadFilename(u.info, u.t.m.cfg.Get().ImageFilenameFormat)
	if err := u.t.m.client.DownloadToFile(ctx, u.info.URL, filepath.Join(dir, name)); err != nil {
		if !errors.Is(err, context.Canceled) {
			u.t.noteError(fmt.Errorf("download %s: %w", name, err))
		}
		return queue.Result{}, err
	}
	u.t.markSaved(u.id)
	return queue.Result{Remove: true}, nil
}

// thumbnailItem downloads a thumbnail used by the saved thread page.
type thumbnailItem struct {
	t   *Thread
	url string
	id  AssetID
}

func (th *thumbnailItem) Context() context.Context { return th.t.ctx }
func (th *thumbnailItem) ShouldProcess() bool {
	return th.t.m.cfg.Get().SaveThumbnails && th.t.ShouldProcess()
}
func (th *thumbnailItem) ReadyAt() time.Time { return time.Time{} }
func (th *thumbnailItem) Order() queue.Order { return th.t.assetOrder(queue.Low) }
func (th *thumbnailItem) GaveUp()            { th.t.markFailed(th.id) }
func (th *thumbnailItem) IsAsset() bool      { return true }
func (th *thumbnailItem) String() string     { return "Thumbnail " + th.id.ID }

func (th *thumbnailItem) Process(ctx context.Context) (queue.Result, error) {
	if !th.ShouldProcess() {
		return queue.Result{Remove: true}, nil
	}
	th.t.active.Add(1)
	defer th.t.active.Done()
	threadDir, err := th.t.ensureDir()
	if err != nil {
		return queue.Result{}, err
	}
	dir := filepath.Join(threadDir, "thumb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return queue.Result{}, err
	}
	if err := th.t.m.client.DownloadToFile(ctx, th.url, filepath.Join(dir, path.Base(th.url))); err != nil {
		return queue.Result{}, err
	}
	th.t.markSaved(th.id)
	return queue.Result{Remove: true}, nil
}
