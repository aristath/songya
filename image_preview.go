package songya

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var imageWorkers = make(chan struct{}, 4)

const imagePreviewBytes = 16 << 20

type previewBuffer struct{ bytes.Buffer }

func (b *previewBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > imagePreviewBytes {
		return 0, fmt.Errorf("Image preview exceeds 16 MiB")
	}
	return b.Buffer.Write(p)
}

// AttachmentImage returns model/browser-readable pixels without modifying the
// original. Small JPEG/PNG images need no external decoder. Other image formats
// (including HEIC) share one bounded conversion path with the web gallery.
func AttachmentImage(ctx context.Context, file *os.File, size int) ([]byte, string, error) {
	size = max(1, min(size, 1600))
	if _, err := file.Seek(0, 0); err != nil {
		return nil, "", err
	}
	cfg, format, err := image.DecodeConfig(file)
	stat, statErr := file.Stat()
	if err == nil && statErr == nil && stat.Size() <= imagePreviewBytes && (format == "png" || format == "jpeg") && cfg.Width <= size && cfg.Height <= size {
		if _, err = file.Seek(0, 0); err != nil {
			return nil, "", err
		}
		_, _, decodeErr := image.Decode(io.LimitReader(file, imagePreviewBytes+1))
		if _, err = file.Seek(0, 0); err != nil {
			return nil, "", err
		}
		data, err := io.ReadAll(io.LimitReader(file, imagePreviewBytes+1))
		if err != nil {
			return nil, "", err
		}
		if decodeErr == nil && len(data) <= imagePreviewBytes {
			return data, "image/" + format, nil
		}
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case imageWorkers <- struct{}{}:
		defer func() { <-imageWorkers }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	executable := "ffmpeg"
	// Installed Songya binaries share their packaged decoder; otherwise use PATH.
	// This also works for MCP children, whose client's PATH may lack HEIC codecs.
	if home, err := os.UserHomeDir(); err == nil {
		packaged := filepath.Join(home, ".local", "lib", "songya", "bin", "ffmpeg")
		if info, err := os.Stat(packaged); err == nil && info.Mode().IsRegular() {
			executable = packaged
		}
	}
	cmd := exec.CommandContext(ctx, executable, "-v", "error", "-threads", "1", "-i", "/proc/self/fd/3", "-frames:v", "1", "-filter_complex", fmt.Sprintf("scale=min(%d\\,iw):min(%d\\,ih):force_original_aspect_ratio=decrease", size, size), "-threads", "1", "-pix_fmt", "rgba", "-f", "image2pipe", "-c:v", "png", "-")
	cmd.ExtraFiles = []*os.File{file}
	var output previewBuffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil || output.Len() == 0 {
		return nil, "", fmt.Errorf("Image preview unavailable; the original attachment is unchanged")
	}
	return output.Bytes(), "image/png", nil
}
