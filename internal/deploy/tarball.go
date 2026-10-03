package deploy

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// archiveBuildContext packs a Dockerfile project into a gzipped
// tarball for transfer to the node — §18's default strategy builds
// on the target node, so the context must travel there first.
//
// .git directories never travel: they are history, not build
// input. Everything else travels as-is, paths relative to the
// project root, so COPY in the Dockerfile behaves exactly as it
// does locally.
func archiveBuildContext(dir string) (io.Reader, error) {
	pr, pw := io.Pipe()
	gz := gzip.NewWriter(pw)
	tw := tar.NewWriter(gz)

	go func() {
		var wErr error
		defer func() {
			tw.Close()
			gz.Close()
			if wErr != nil {
				pw.CloseWithError(wErr)
			} else {
				pw.Close()
			}
		}()

		wErr = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, rErr := filepath.Rel(dir, p)
			if rErr != nil {
				return rErr
			}
			if rel == "." {
				return nil
			}
			// .git is history, not build input
			if info.IsDir() && rel == ".git" {
				return filepath.SkipDir
			}
			if strings.HasPrefix(rel, ".git/") {
				return nil
			}

			name := filepath.ToSlash(rel)
			if info.IsDir() {
				return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: int64(info.Mode().Perm()), Typeflag: tar.TypeDir})
			}
			if !info.Mode().IsRegular() {
				// sockets, devices, and fifos have no business in an
				// image; symlinks travel as their target's content
				if info.Mode()&os.ModeSymlink != 0 {
					target, lErr := os.Readlink(p)
					if lErr != nil {
						return lErr
					}
					resolved := filepath.Join(filepath.Dir(p), target)
					rInfo, rErr := os.Stat(resolved)
					if rErr != nil {
						return rErr
					}
					info = rInfo
					p = resolved
				} else {
					return nil
				}
			}

			f, oErr := os.Open(p)
			if oErr != nil {
				return oErr
			}
			defer f.Close()
			if hErr := tw.WriteHeader(&tar.Header{
				Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(),
			}); hErr != nil {
				return hErr
			}
			_, wErr := io.Copy(tw, f)
			return wErr
		})
	}()

	return pr, nil
}
