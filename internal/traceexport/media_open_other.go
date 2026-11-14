//go:build !unix

package traceexport

import (
	"fmt"
	"os"
	"runtime"
)

// openMediaSource refuses media copies on platforms without the Unix
// openat/O_NOFOLLOW walk instead of silently following symlinks.
func openMediaSource(workspace, rel string) (*os.File, error) {
	return nil, fmt.Errorf("secure media export is not supported on %s", runtime.GOOS)
}
