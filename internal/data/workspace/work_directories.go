package workspace

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"golang.org/x/sys/unix"
)

// The mutable cache and temporary scopes are execution-owned PVC directories,
// separate from all roots scanned for published artifacts.
func prepareWorkDirectories(ctx context.Context, root int, subpath string) error {
	if subpath != "work" {
		return biz.ErrInvalidWorkspaceOutput
	}
	for _, role := range []string{"train", "prepare", "export", "verify", "collect", "publish"} {
		for _, directory := range []string{"tmp", "hf/datasets", "matplotlib"} {
			if err := ctx.Err(); err != nil {
				return err
			}
			fd, err := openPipelineSubdirectory(root, subpath+"/"+role+"/"+directory)
			if err != nil {
				return err
			}
			unix.Close(fd)
		}
	}
	return unix.Fsync(root)
}
