package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveBuildContext states where a dockerfile deploy's build
// context is: a directory value is the context itself (the draft's
// "dockerfile: ."), a file value's containing directory is. The
// context must live inside the project — a manifest pointing
// outside would stream directories the project does not contain,
// and a fifteen-minute transfer of the wrong tree is exactly the
// mistake the refusal prevents at the start.
func ResolveBuildContext(projectDir, dockerfile string) (string, error) {
	if dockerfile == "" {
		return "", fmt.Errorf("the manifest names no dockerfile to build")
	}
	joined := filepath.Join(projectDir, dockerfile)
	if info, err := os.Stat(joined); err != nil || !info.IsDir() {
		// not a directory on disk: the value names the Dockerfile,
		// and the context is where it lives
		joined = filepath.Dir(joined)
	}
	ctx, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	if ctx != root && !strings.HasPrefix(ctx+string(filepath.Separator), root+string(filepath.Separator)) {
		return "", fmt.Errorf("the build context %s sits outside the project %s — mymo will not ship what the project does not contain", ctx, root)
	}
	return ctx, nil
}
