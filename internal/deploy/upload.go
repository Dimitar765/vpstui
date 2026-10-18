package deploy

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
)

// uploadTimeout bounds every single SFTP round trip; a wedged link fails a
// deploy instead of pinning a worker forever.
const uploadTimeout = 30 * time.Second

// WriteFileAtomic writes data to path over SFTP the way config.WriteFileAtomic
// does locally: temp file in the same directory, chmod to perm, then
// PosixRename over the target. Servers whose SFTP implementation lacks
// PosixRename fall back to plain Rename plus a post-rename chmod (the rename
// would otherwise reset an existing file's mode).
func WriteFileAtomic(cl *sftp.Client, path string, data []byte, perm fs.FileMode) error {
	tmp := path + ".tmp"
	f, err := cl.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("sftp open %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("sftp write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sftp close %s: %w", tmp, err)
	}
	if err := cl.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("sftp chmod %s: %w", tmp, err)
	}
	if err := cl.PosixRename(tmp, path); err != nil {
		// Rare (OpenSSH and the podman shim both support it): retry with a
		// plain rename — mode already set above, so no post-chmod needed.
		if rerr := cl.Rename(tmp, path); rerr != nil {
			_ = cl.Remove(tmp)
			return fmt.Errorf("sftp rename %s -> %s: %w (posix: %v)", tmp, path, rerr, err)
		}
	}
	return nil
}

// UploadDir copies the local filesystem tree rooted at local into remoteDir
// over SFTP, creating directories with 0750 and files preserving their local
// mode. Regular files only — anything else in the tree is skipped.
func UploadDir(cl *sftp.Client, local fs.FS, remoteDir string) error {
	if err := cl.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("sftp mkdir %s: %w", remoteDir, err)
	}
	return fs.WalkDir(local, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		remote := path.Join(remoteDir, p)
		if d.IsDir() {
			if err := cl.MkdirAll(remote); err != nil {
				return fmt.Errorf("sftp mkdir %s: %w", remote, err)
			}
			return cl.Chmod(remote, 0o750)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		src, err := local.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := cl.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return fmt.Errorf("sftp open %s: %w", remote, err)
		}
		defer dst.Close()
		if _, err := io.Copy(dst, src); err != nil {
			return fmt.Errorf("sftp copy %s: %w", remote, err)
		}
		return cl.Chmod(remote, info.Mode().Perm())
	})
}
